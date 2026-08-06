package tools

import (
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// CommandFacts is a conservative, argument-free summary of a shell command. It
// is suitable for JSON event metadata; it intentionally never stores command
// arguments, redirection targets, or command-substitution text. Subcommand is a
// normalized allowlisted category rather than a raw argument.
//
// Dangerous and Sensitive are two distinct tiers. Dangerous marks catastrophic,
// effectively irreversible actions and is hard-blocked in every permission mode.
// Sensitive marks serious but recoverable actions that must always reach a human
// approval prompt but must remain approvable, so that routine development work
// (force-push, package install, service restart) stays possible.
type CommandFacts struct {
	ParseKnown   bool     `json:"parseKnown"`
	Program      string   `json:"program,omitempty"`
	Subcommand   string   `json:"subcommand,omitempty"`
	CommandCount int      `json:"commandCount"`
	Compound     bool     `json:"compound"`
	Pipeline     bool     `json:"pipeline"`
	Redirection  bool     `json:"redirection"`
	Substitution bool     `json:"substitution"`
	Background   bool     `json:"background"`
	Effects      []string `json:"effects,omitempty"`
	Dangerous    []string `json:"dangerous,omitempty"`
	Sensitive    []string `json:"sensitive,omitempty"`
}

// Unclassified reports whether static analysis failed to determine what a
// command actually does. Callers must treat this as a reason to require human
// approval rather than as evidence that the command is safe.
func (f CommandFacts) Unclassified() bool {
	return !f.ParseKnown || f.Program == "dynamic" || f.Program == "other"
}

// NeedsApproval reports whether a command must never execute silently, even in
// permission modes that otherwise auto-approve execution.
func (f CommandFacts) NeedsApproval() bool {
	return f.Unclassified() || len(f.Sensitive) > 0 || len(f.Dangerous) > 0
}

type bashCommandAnalysis struct {
	facts   CommandFacts
	warning string
}

// AnalyzeBashCommand returns a conservative, JSON-safe summary of command,
// analyzed with the grammar of the shell that BashTool actually executes on this
// platform: cmd.exe on Windows, POSIX sh elsewhere.
func AnalyzeBashCommand(command string) CommandFacts {
	return analyzeBashCommand(command).facts
}

func analyzeBashCommand(command string) bashCommandAnalysis {
	var facts CommandFacts
	if runtime.GOOS == "windows" {
		// BashTool runs `cmd /C` here, so cmd.exe and PowerShell are the real
		// grammar. Running the POSIX parser instead would leave every native
		// destructive verb unclassified.
		facts = analyzeWindowsCommand(command)
		// POSIX tooling is still reachable on Windows through Git for Windows and
		// similar bundles, so keep the POSIX string checks as defense in depth.
		if label := legacyDangerLabel(command); label != "" {
			facts.Dangerous = mergeFactLabels(facts.Dangerous, []string{label})
		}
	} else {
		facts = analyzePOSIXShell(command, 0)
	}
	warning := commandDangerWarningFromFacts(facts)
	if warning == "" {
		// Retain the established string checks as a defense-in-depth fallback
		// for malformed, non-POSIX, and otherwise unclassified input.
		warning = legacyBashDangerWarning(command)
	}
	return bashCommandAnalysis{facts: facts, warning: warning}
}

func analyzePOSIXShell(command string, depth int) CommandFacts {
	var facts CommandFacts
	file, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader(command), "command")
	if err != nil {
		return facts
	}
	facts.ParseKnown = true
	if len(file.Stmts) > 1 {
		facts.Compound = true
	}

	collector := commandFactsCollector{facts: &facts, effects: make(map[string]struct{}), dangerous: make(map[string]struct{}), sensitive: make(map[string]struct{})}
	syntax.Walk(file, collector.visit)
	collector.finish()

	if depth < maxNestedShellAnalysisDepth {
		for _, nested := range collector.nestedScripts {
			nestedFacts := analyzePOSIXShell(nested.script, depth+1)
			if !nestedFacts.ParseKnown || nested.nonPOSIX {
				facts.ParseKnown = false
			}
			facts.CommandCount += nestedFacts.CommandCount
			facts.Compound = facts.Compound || nestedFacts.Compound
			facts.Pipeline = facts.Pipeline || nestedFacts.Pipeline
			facts.Redirection = facts.Redirection || nestedFacts.Redirection
			facts.Substitution = facts.Substitution || nestedFacts.Substitution
			facts.Background = facts.Background || nestedFacts.Background
			facts.Effects = mergeFactLabels(facts.Effects, nestedFacts.Effects)
			facts.Dangerous = mergeFactLabels(facts.Dangerous, nestedFacts.Dangerous)
			facts.Sensitive = mergeFactLabels(facts.Sensitive, nestedFacts.Sensitive)
		}
	} else if len(collector.nestedScripts) > 0 {
		facts.ParseKnown = false
	}
	return facts
}

const maxNestedShellAnalysisDepth = 2

type nestedShellScript struct {
	script   string
	nonPOSIX bool
}

type commandFactsCollector struct {
	facts         *CommandFacts
	effects       map[string]struct{}
	dangerous     map[string]struct{}
	sensitive     map[string]struct{}
	nestedScripts []nestedShellScript
}

func (c *commandFactsCollector) visit(node syntax.Node) bool {
	switch node := node.(type) {
	case *syntax.Stmt:
		if node.Background {
			c.facts.Background = true
		}
	case *syntax.CallExpr:
		c.visitCall(node)
	case *syntax.BinaryCmd:
		c.facts.Compound = true
		if node.Op.String() == "|" {
			c.facts.Pipeline = true
			c.visitPipeline(node)
		}
	case *syntax.Redirect:
		c.facts.Redirection = true
		if truncatesRedirect(node) {
			c.effect("filesystem-write")
			c.danger("file-truncate")
		}
	case *syntax.CmdSubst:
		c.facts.Substitution = true
		c.facts.Compound = true
	case *syntax.Subshell, *syntax.Block, *syntax.IfClause, *syntax.WhileClause, *syntax.ForClause, *syntax.FuncDecl:
		c.facts.Compound = true
	}
	return true
}

func (c *commandFactsCollector) visitCall(call *syntax.CallExpr) {
	c.facts.CommandCount++
	if len(call.Args) == 0 {
		return
	}
	program, knownProgram := safeStaticWord(call.Args[0])
	if c.facts.Program == "" {
		if knownProgram {
			c.facts.Program = safeProgramName(program)
		} else {
			c.facts.Program = "dynamic"
		}
	}
	if !knownProgram {
		// A variable- or substitution-derived program name cannot be classified,
		// so the command must not be treated as analyzed-and-safe.
		c.facts.ParseKnown = false
		return
	}
	program = safeProgramName(program)
	if program == "other" || program == "dynamic" {
		c.facts.ParseKnown = false
		return
	}
	if c.facts.Subcommand == "" && isSubcommandProgram(program) && len(call.Args) > 1 {
		if subcommand, ok := safeStaticWord(call.Args[1]); ok {
			c.facts.Subcommand = stableSubcommand(program, subcommand)
		}
	}
	c.inspectSemanticCall(program, call.Args[1:], 0)
}

const maxWrappedCommandAnalysisDepth = 4

func (c *commandFactsCollector) inspectSemanticCall(program string, args []*syntax.Word, depth int) {
	c.classifyCall(program, args)
	c.collectNestedShell(program, args)
	if depth >= maxWrappedCommandAnalysisDepth {
		return
	}
	if program == "find" {
		for _, nested := range findExecCommands(args) {
			if !nested.known {
				c.facts.ParseKnown = false
				continue
			}
			c.effect("shell-execution")
			c.inspectSemanticCall(nested.program, nested.args, depth+1)
		}
	}
	if program == "eval" {
		script, ok := staticJoinedWords(args)
		if !ok {
			c.facts.ParseKnown = false
			return
		}
		c.effect("shell-execution")
		c.facts.Compound = true
		c.nestedScripts = append(c.nestedScripts, nestedShellScript{script: script})
		return
	}
	wrappedProgram, wrappedArgs, wrapped, known := unwrapStaticCommand(program, args)
	if !known {
		c.facts.ParseKnown = false
		return
	}
	if !wrapped {
		return
	}
	c.effect("shell-execution")
	c.inspectSemanticCall(wrappedProgram, wrappedArgs, depth+1)
}

func (c *commandFactsCollector) classifyCall(program string, args []*syntax.Word) {
	switch program {
	case "rm", "rmdir", "mv":
		if program != "mv" || hasStaticArgument(args, "/dev/null") {
			c.effect("filesystem-delete")
			c.danger("file-delete")
		}
	case "unlink":
		// The direct synonym of rm for a single file.
		c.effect("filesystem-delete")
		c.danger("file-delete")
	case "truncate":
		if hasTruncateToZeroArgument(args) {
			c.effect("filesystem-write")
			c.danger("file-truncate")
		} else {
			c.effect("filesystem-write")
			c.sensitiveLabel("file-truncate-scoped")
		}
	case "sudo", "doas", "pkexec":
		c.effect("privileged-execution")
		c.danger("privilege-escalation")
	case "dd":
		c.effect("disk-write")
		c.danger("disk-write")
	case "shred":
		c.effect("filesystem-delete")
		c.danger("file-destroy")
	case "wipefs", "blkdiscard", "sgdisk", "fdisk", "parted", "cfdisk", "mkswap", "hdparm", "nvme":
		c.effect("disk-write")
		c.danger("disk-partition")
	case "find":
		if hasStaticArgument(args, "-delete") {
			c.effect("filesystem-delete")
			c.danger("find-delete")
		}
	case "git":
		c.classifyGitCall(args)
	case "chmod":
		c.classifyChmodCall(args)
	case "chown", "chgrp":
		c.effect("permission-change")
		if hasRecursiveArgument(args) {
			c.danger("permission-weaken")
		} else {
			c.sensitiveLabel("permission-change")
		}
	case "kill", "pkill", "killall":
		c.effect("process-kill")
		// `kill -9 -1` and `killall -9` annihilate every process the user owns.
		if hasStaticArgument(args, "-1") || program == "killall" {
			c.danger("process-kill-all")
		} else {
			c.sensitiveLabel("process-kill")
		}
	case "crontab":
		c.effect("scheduled-task-change")
		if hasStaticArgument(args, "-r") {
			c.danger("crontab-delete")
		} else {
			c.sensitiveLabel("scheduled-task-inspect")
		}
	case "iptables", "ip6tables", "nft", "ufw", "firewall-cmd":
		c.effect("network-config")
		if hasStaticArgument(args, "-F") || hasStaticArgument(args, "flush") {
			c.danger("firewall-flush")
		} else {
			c.sensitiveLabel("network-config")
		}
	case "systemctl", "service", "launchctl":
		c.effect("service-change")
		c.sensitiveLabel("service-change")
	case "docker", "podman", "nerdctl":
		if hasStaticArgument(args, "prune") || (hasStaticArgument(args, "rm") || hasStaticArgument(args, "rmi")) && hasForceArgument(args) {
			c.effect("container-delete")
			c.danger("container-delete")
		}
	case "npm", "pnpm", "yarn", "bun", "pip", "pip3", "gem", "cargo":
		// Installs execute arbitrary lifecycle code, but they are routine in a
		// development IDE, so they require approval rather than a hard block.
		if hasStaticArgument(args, "install") || hasStaticArgument(args, "add") || hasStaticArgument(args, "ci") {
			c.effect("package-install")
			c.sensitiveLabel("package-install")
		}
	case "tee":
		c.effect("filesystem-write")
		c.sensitiveLabel("file-overwrite")
	case "mount", "umount":
		c.effect("filesystem-mount")
		c.sensitiveLabel("filesystem-mount")
	case "shutdown", "reboot", "halt", "poweroff":
		c.effect("system-shutdown")
		c.danger("system-shutdown")
	}
	if strings.HasPrefix(program, "mkfs") {
		c.effect("disk-format")
		c.danger("disk-format")
	}
}

func (c *commandFactsCollector) classifyGitCall(args []*syntax.Word) {
	switch {
	case hasStaticArgument(args, "clean") && hasForceArgument(args):
		c.effect("filesystem-delete")
		c.danger("git-clean")
	case hasStaticArgument(args, "reset") && hasStaticArgument(args, "--hard"):
		c.effect("repository-state-discard")
		c.danger("git-reset-hard")
	case hasStaticArgument(args, "push") && hasForceArgument(args) && !hasStaticArgument(args, "--force-with-lease"):
		c.effect("repository-history-rewrite")
		c.sensitiveLabel("git-force-push")
	case hasStaticArgument(args, "filter-branch") || hasStaticArgument(args, "filter-repo"):
		c.effect("repository-history-rewrite")
		c.sensitiveLabel("git-history-rewrite")
	case (hasStaticArgument(args, "checkout") || hasStaticArgument(args, "restore")) && hasForceArgument(args):
		c.effect("repository-state-discard")
		c.sensitiveLabel("git-discard-changes")
	}
}

// classifyChmodCall flags permission changes that broadly weaken access or add
// setuid, not only the single literal 777 case.
func (c *commandFactsCollector) classifyChmodCall(args []*syntax.Word) {
	mode, ok := chmodModeArgument(args)
	if !ok {
		return
	}
	c.effect("permission-change")
	weakening := mode == "777" || mode == "666" || mode == "a+rwx" || strings.Contains(mode, "o+w")
	setuid := strings.HasPrefix(mode, "4") && len(mode) == 4 || strings.Contains(mode, "u+s") || strings.Contains(mode, "g+s")
	lockout := mode == "000"
	switch {
	case hasRecursiveArgument(args) && (weakening || lockout):
		c.danger("permission-weaken")
	case setuid:
		c.danger("permission-setuid")
	case weakening || lockout:
		c.sensitiveLabel("permission-change")
	default:
		c.sensitiveLabel("permission-change")
	}
}

// chmodModeArgument returns the first non-flag operand, which is the mode.
func chmodModeArgument(args []*syntax.Word) (string, bool) {
	for _, arg := range args {
		value, ok := safeStaticWord(arg)
		if !ok {
			return "", false
		}
		if value == "--" {
			continue
		}
		if strings.HasPrefix(value, "-") {
			continue
		}
		return value, true
	}
	return "", false
}

// hasTruncateToZeroArgument detects `truncate -s 0` and `truncate -s0`, which
// destroy file contents in place.
func hasTruncateToZeroArgument(args []*syntax.Word) bool {
	for index, arg := range args {
		value, ok := safeStaticWord(arg)
		if !ok {
			continue
		}
		if value == "-s" || value == "--size" {
			if index+1 < len(args) {
				if next, ok := safeStaticWord(args[index+1]); ok && isZeroSize(next) {
					return true
				}
			}
			continue
		}
		if strings.HasPrefix(value, "-s") && isZeroSize(strings.TrimPrefix(value, "-s")) {
			return true
		}
		if strings.HasPrefix(value, "--size=") && isZeroSize(strings.TrimPrefix(value, "--size=")) {
			return true
		}
	}
	return false
}

func isZeroSize(value string) bool {
	return value == "0" || value == "00"
}

func (c *commandFactsCollector) collectNestedShell(program string, args []*syntax.Word) {
	if !isShellProgram(program) {
		return
	}
	for i := 0; i < len(args); i++ {
		flag, ok := safeStaticWord(args[i])
		if !ok {
			c.facts.ParseKnown = false
			continue
		}
		if !shellCommandStringFlag(flag) {
			continue
		}
		if i+1 >= len(args) {
			c.facts.ParseKnown = false
			return
		}
		script, ok := safeStaticWord(args[i+1])
		if !ok {
			c.facts.ParseKnown = false
			return
		}
		c.effect("nested-shell")
		c.facts.Compound = true
		c.nestedScripts = append(c.nestedScripts, nestedShellScript{script: script, nonPOSIX: program != "sh" && program != "dash"})
		return
	}
}

func shellCommandStringFlag(flag string) bool {
	if flag == "-c" {
		return true
	}
	return len(flag) > 2 && flag[0] == '-' && flag[1] != '-' && strings.ContainsRune(flag[1:], 'c')
}

func staticJoinedWords(words []*syntax.Word) (string, bool) {
	values := make([]string, 0, len(words))
	for _, word := range words {
		value, ok := safeStaticWord(word)
		if !ok {
			return "", false
		}
		values = append(values, value)
	}
	return strings.Join(values, " "), true
}

func unwrapStaticCommand(program string, args []*syntax.Word) (string, []*syntax.Word, bool, bool) {
	switch program {
	case "command":
		return unwrapCommandBuiltin(args)
	case "env":
		return unwrapEnvCommand(args)
	case "exec":
		return unwrapExecCommand(args)
	case "nohup":
		return unwrapFirstStaticCommand(args)
	case "xargs":
		return unwrapXargsCommand(args)
	default:
		if isExecWrapperProgram(program) {
			return unwrapExecWrapperCommand(program, args)
		}
		return "", nil, false, true
	}
}

// isExecWrapperProgram reports whether a program's purpose is to run another
// command with modified scheduling, timing, or session context. Without these,
// `timeout 5 rm -rf /` would classify only as `timeout` and evade every rule.
func isExecWrapperProgram(program string) bool {
	switch program {
	case "timeout", "gtimeout", "setsid", "stdbuf", "unbuffer", "nice", "ionice",
		"chrt", "taskset", "flock", "watch", "script", "time", "strace", "ltrace",
		"proot", "eatmydata", "torify", "torsocks", "systemd-run", "runuser", "su":
		return true
	default:
		return false
	}
}

// execWrapperValueFlags lists wrapper options that consume a following operand,
// so the scanner does not mistake that operand for the wrapped program.
func execWrapperValueFlags(program string) map[string]struct{} {
	switch program {
	case "nice", "ionice":
		return map[string]struct{}{"-n": {}, "--adjustment": {}, "-c": {}, "-p": {}}
	case "chrt":
		return map[string]struct{}{"-p": {}}
	case "taskset":
		return map[string]struct{}{"-c": {}, "-p": {}}
	case "stdbuf":
		return map[string]struct{}{"-i": {}, "-o": {}, "-e": {}}
	case "timeout", "gtimeout":
		return map[string]struct{}{"-s": {}, "--signal": {}, "-k": {}, "--kill-after": {}}
	case "watch":
		return map[string]struct{}{"-n": {}, "--interval": {}}
	case "flock":
		return map[string]struct{}{"-w": {}, "--wait": {}, "-E": {}}
	case "script":
		return map[string]struct{}{"-c": {}, "--command": {}}
	case "systemd-run", "runuser", "su":
		return map[string]struct{}{"-u": {}, "--unit": {}, "-p": {}, "--property": {}, "-l": {}}
	default:
		return map[string]struct{}{}
	}
}

// unwrapExecWrapperCommand finds the wrapped program inside an exec wrapper.
// It fails closed (known=false) whenever an argument cannot be read statically,
// so an unreadable wrapper never silently downgrades to "not dangerous".
func unwrapExecWrapperCommand(program string, args []*syntax.Word) (string, []*syntax.Word, bool, bool) {
	valueFlags := execWrapperValueFlags(program)
	// flock takes a lock file or descriptor operand before the command it runs.
	pendingOperands := 0
	if program == "flock" {
		pendingOperands = 1
	}
	// `script -c "<script>"` and `watch "<script>"` take a shell string rather
	// than an argv, so the caller handles them as nested shell scripts instead.
	for index := 0; index < len(args); index++ {
		value, ok := safeStaticWord(args[index])
		if !ok {
			return "", nil, false, false
		}
		if value == "--" {
			return unwrapFirstStaticCommand(args[index+1:])
		}
		if _, needsValue := valueFlags[value]; needsValue {
			index++
			if index >= len(args) {
				return "", nil, false, false
			}
			continue
		}
		if strings.HasPrefix(value, "-") {
			// Attached-value or boolean flag; skip it.
			continue
		}
		// `timeout` takes a bare duration operand before the command.
		if (program == "timeout" || program == "gtimeout") && isDurationOperand(value) {
			continue
		}
		if pendingOperands > 0 {
			pendingOperands--
			continue
		}
		wrapped := safeProgramName(value)
		return wrapped, args[index+1:], wrapped != "dynamic" && wrapped != "other", wrapped != "dynamic" && wrapped != "other"
	}
	return "", nil, false, true
}

func isDurationOperand(value string) bool {
	if value == "" {
		return false
	}
	trimmed := strings.TrimRight(value, "smhd")
	if trimmed == "" {
		return false
	}
	for _, char := range trimmed {
		if (char < '0' || char > '9') && char != '.' {
			return false
		}
	}
	return true
}

func unwrapCommandBuiltin(args []*syntax.Word) (string, []*syntax.Word, bool, bool) {
	for index, arg := range args {
		value, ok := safeStaticWord(arg)
		if !ok {
			return "", nil, false, false
		}
		switch value {
		case "--":
			return unwrapFirstStaticCommand(args[index+1:])
		case "-p":
			continue
		case "-v", "-V":
			return "", nil, false, true
		}
		if strings.HasPrefix(value, "-") {
			return "", nil, false, false
		}
		program := safeProgramName(value)
		return program, args[index+1:], program != "dynamic", program != "dynamic"
	}
	return "", nil, false, true
}

func unwrapEnvCommand(args []*syntax.Word) (string, []*syntax.Word, bool, bool) {
	for index := 0; index < len(args); index++ {
		value, ok := safeStaticWord(args[index])
		if !ok {
			return "", nil, false, false
		}
		switch {
		case value == "--":
			return unwrapFirstStaticCommand(args[index+1:])
		case value == "-i" || value == "--ignore-environment" || value == "-0" || value == "--null" || value == "-v" || value == "--debug":
			continue
		case value == "-u" || value == "--unset" || value == "-C" || value == "--chdir":
			index++
			if index >= len(args) {
				return "", nil, false, false
			}
			continue
		case strings.HasPrefix(value, "--unset=") || strings.HasPrefix(value, "--chdir="):
			continue
		case value == "-S" || value == "--split-string" || strings.HasPrefix(value, "--split-string="):
			return "", nil, false, false
		case strings.HasPrefix(value, "-"):
			return "", nil, false, false
		case strings.Contains(value, "="):
			continue
		default:
			program := safeProgramName(value)
			return program, args[index+1:], program != "dynamic", program != "dynamic"
		}
	}
	return "", nil, false, true
}

func unwrapExecCommand(args []*syntax.Word) (string, []*syntax.Word, bool, bool) {
	for index := 0; index < len(args); index++ {
		value, ok := safeStaticWord(args[index])
		if !ok {
			return "", nil, false, false
		}
		if value == "--" {
			return unwrapFirstStaticCommand(args[index+1:])
		}
		if value == "-a" {
			index++
			if index >= len(args) {
				return "", nil, false, false
			}
			continue
		}
		if strings.HasPrefix(value, "-") {
			if strings.Trim(value[1:], "cl") != "" {
				return "", nil, false, false
			}
			continue
		}
		program := safeProgramName(value)
		return program, args[index+1:], program != "dynamic", program != "dynamic"
	}
	return "", nil, false, true
}

func unwrapFirstStaticCommand(args []*syntax.Word) (string, []*syntax.Word, bool, bool) {
	if len(args) == 0 {
		return "", nil, false, true
	}
	index := 0
	value, ok := safeStaticWord(args[index])
	if !ok {
		return "", nil, false, false
	}
	if value == "--" {
		index++
		if index >= len(args) {
			return "", nil, false, true
		}
		value, ok = safeStaticWord(args[index])
		if !ok {
			return "", nil, false, false
		}
	}
	program := safeProgramName(value)
	return program, args[index+1:], program != "dynamic", program != "dynamic"
}

func unwrapXargsCommand(args []*syntax.Word) (string, []*syntax.Word, bool, bool) {
	for index := 0; index < len(args); index++ {
		value, ok := safeStaticWord(args[index])
		if !ok {
			return "", nil, false, false
		}
		switch {
		case value == "--":
			return unwrapFirstStaticCommand(args[index+1:])
		case value == "-0" || value == "--null" || value == "-r" || value == "--no-run-if-empty" || value == "-t" || value == "--verbose" || value == "-p" || value == "--interactive" || value == "-x" || value == "--exit" || value == "-o" || value == "--open-tty" || value == "-e" || value == "-i" || value == "-l":
			continue
		case xargsOptionNeedsValue(value):
			index++
			if index >= len(args) {
				return "", nil, false, false
			}
			continue
		case xargsOptionHasAttachedValue(value):
			continue
		case strings.HasPrefix(value, "--eof=") || strings.HasPrefix(value, "--replace=") || strings.HasPrefix(value, "--max-lines=") || strings.HasPrefix(value, "--max-args=") || strings.HasPrefix(value, "--max-procs=") || strings.HasPrefix(value, "--max-chars=") || strings.HasPrefix(value, "--delimiter=") || strings.HasPrefix(value, "--arg-file="):
			continue
		case strings.HasPrefix(value, "-"):
			return "", nil, false, false
		default:
			program := safeProgramName(value)
			return program, args[index+1:], program != "dynamic", program != "dynamic"
		}
	}
	return "", nil, false, true
}

func xargsOptionNeedsValue(value string) bool {
	switch value {
	case "-E", "--eof", "-I", "--replace", "-L", "--max-lines", "-n", "--max-args", "-P", "--max-procs", "-s", "--max-chars", "-d", "--delimiter", "-a", "--arg-file", "-J", "-R", "-S":
		return true
	default:
		return false
	}
}

func xargsOptionHasAttachedValue(value string) bool {
	if len(value) <= 2 || value[0] != '-' || value[1] == '-' {
		return false
	}
	return strings.ContainsRune("EeIiLlnPsdaJRS", rune(value[1]))
}

type staticNestedCommand struct {
	program string
	args    []*syntax.Word
	known   bool
}

func findExecCommands(args []*syntax.Word) []staticNestedCommand {
	commands := make([]staticNestedCommand, 0)
	for index := 0; index < len(args); index++ {
		operator, ok := safeStaticWord(args[index])
		if !ok || !oneOfString(operator, "-exec", "-execdir", "-ok", "-okdir") {
			continue
		}
		if index+1 >= len(args) {
			commands = append(commands, staticNestedCommand{})
			break
		}
		programValue, ok := safeStaticWord(args[index+1])
		if !ok {
			commands = append(commands, staticNestedCommand{})
			continue
		}
		end := index + 2
		for end < len(args) {
			value, static := safeStaticWord(args[end])
			if static && (value == ";" || value == "+") {
				break
			}
			end++
		}
		program := safeProgramName(programValue)
		commands = append(commands, staticNestedCommand{program: program, args: args[index+2 : end], known: program != "dynamic"})
		index = end
	}
	return commands
}

func oneOfString(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func (c *commandFactsCollector) visitPipeline(binary *syntax.BinaryCmd) {
	left, leftOK := commandProgram(binary.X)
	right, rightOK := commandProgram(binary.Y)
	if leftOK && isNetworkFetchProgram(left) {
		c.effect("network-access")
	}
	if !rightOK || !isInterpreterProgram(right) {
		return
	}
	// Any pipeline that ends in an interpreter is executing whatever the left
	// side produced. Piping a download into python is the same remote-code-
	// execution shape as piping it into sh, and a decode step in between
	// (base64, xxd, openssl) only obscures the same thing.
	if leftOK && isNetworkFetchProgram(left) {
		c.effect("shell-execution")
		c.danger("network-pipe-shell")
		return
	}
	if leftOK && isDecoderProgram(left) {
		c.effect("shell-execution")
		c.danger("decoded-pipe-shell")
		return
	}
	// A multi-stage left side (`curl x | base64 -d | sh`) parses as a nested
	// pipeline, so the immediate left program is not a plain call. Inspect the
	// whole left subtree before falling back to the weaker label.
	if pipelineContainsProgram(binary.X, isNetworkFetchProgram) {
		c.effect("shell-execution")
		c.danger("network-pipe-shell")
		return
	}
	if pipelineContainsProgram(binary.X, isDecoderProgram) {
		c.effect("shell-execution")
		c.danger("decoded-pipe-shell")
		return
	}
	c.effect("shell-execution")
	c.sensitiveLabel("pipe-to-interpreter")
}

// pipelineContainsProgram reports whether any stage of a nested left-hand
// pipeline runs a program matching predicate, so `curl x | base64 -d | sh` is
// still recognized as remote code execution.
func pipelineContainsProgram(stmt *syntax.Stmt, predicate func(string) bool) bool {
	if stmt == nil {
		return false
	}
	found := false
	syntax.Walk(stmt, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		if program, ok := safeStaticWord(call.Args[0]); ok && predicate(safeProgramName(program)) {
			found = true
			return false
		}
		return true
	})
	return found
}

func isNetworkFetchProgram(program string) bool {
	switch program {
	case "curl", "wget", "fetch", "aria2c", "httpie", "http":
		return true
	default:
		return false
	}
}

func isDecoderProgram(program string) bool {
	switch program {
	case "base64", "base32", "xxd", "uudecode", "openssl", "gunzip", "zcat", "gzip":
		return true
	default:
		return false
	}
}

// isInterpreterProgram reports whether a program executes code supplied on
// stdin. It deliberately covers more than shells.
func isInterpreterProgram(program string) bool {
	if isShellProgram(program) {
		return true
	}
	switch program {
	case "python", "python2", "python3", "perl", "ruby", "node", "deno", "bun",
		"php", "lua", "tclsh", "rscript", "osascript", "groovy", "julia", "elixir":
		return true
	default:
		return false
	}
}

func (c *commandFactsCollector) effect(label string) {
	c.effects[label] = struct{}{}
}

func (c *commandFactsCollector) danger(label string) {
	c.dangerous[label] = struct{}{}
}

func (c *commandFactsCollector) sensitiveLabel(label string) {
	c.sensitive[label] = struct{}{}
}

func (c *commandFactsCollector) finish() {
	c.facts.Effects = sortedFactLabels(c.effects)
	c.facts.Dangerous = sortedFactLabels(c.dangerous)
	c.facts.Sensitive = sortedFactLabels(c.sensitive)
}

func commandProgram(stmt *syntax.Stmt) (string, bool) {
	if stmt == nil {
		return "", false
	}
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return "", false
	}
	program, ok := safeStaticWord(call.Args[0])
	if !ok {
		return "", false
	}
	return safeProgramName(program), true
}

func safeStaticWord(word *syntax.Word) (string, bool) {
	if word == nil || len(word.Parts) == 0 {
		return "", false
	}
	var value strings.Builder
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			value.WriteString(part.Value)
		case *syntax.SglQuoted:
			value.WriteString(part.Value)
		case *syntax.DblQuoted:
			for _, quotedPart := range part.Parts {
				literal, ok := quotedLiteralPart(quotedPart)
				if !ok {
					return "", false
				}
				value.WriteString(literal)
			}
		default:
			return "", false
		}
	}
	return value.String(), true
}

func quotedLiteralPart(part syntax.WordPart) (string, bool) {
	switch part := part.(type) {
	case *syntax.Lit:
		return part.Value, true
	case *syntax.SglQuoted:
		return part.Value, true
	default:
		return "", false
	}
}

const maxCommandFactProgramBytes = 64

func safeProgramName(program string) string {
	program = strings.TrimSpace(program)
	// A leading backslash is the shell's alias-bypass form: `\rm` runs the real
	// rm. Strip it so the command classifies as rm instead of collapsing to the
	// unclassified "other" bucket.
	program = strings.TrimLeft(program, `\`)
	program = filepath.Base(program)
	if program == "." || program == string(filepath.Separator) {
		return "dynamic"
	}
	if len(program) == 0 || len(program) > maxCommandFactProgramBytes || !safeProgramToken(program) {
		return "other"
	}
	return program
}

func safeProgramToken(program string) bool {
	for index := 0; index < len(program); index++ {
		char := program[index]
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			continue
		}
		if index > 0 && (char == '.' || char == '_' || char == '+' || char == '-') {
			continue
		}
		return false
	}
	return true
}

func isSubcommandProgram(program string) bool {
	switch program {
	case "git", "go", "npm", "pnpm", "yarn", "bun":
		return true
	default:
		return false
	}
}

func stableSubcommand(program, subcommand string) string {
	allowed := map[string]map[string]struct{}{
		"git":  {"add": {}, "branch": {}, "checkout": {}, "clean": {}, "clone": {}, "commit": {}, "config": {}, "diff": {}, "fetch": {}, "log": {}, "merge": {}, "pull": {}, "push": {}, "reset": {}, "restore": {}, "show": {}, "status": {}, "switch": {}, "tag": {}},
		"go":   {"build": {}, "clean": {}, "env": {}, "fmt": {}, "generate": {}, "get": {}, "install": {}, "list": {}, "mod": {}, "run": {}, "test": {}, "tool": {}, "vet": {}, "version": {}, "work": {}},
		"npm":  {"ci": {}, "exec": {}, "install": {}, "run": {}, "test": {}, "update": {}},
		"pnpm": {"add": {}, "build": {}, "install": {}, "lint": {}, "run": {}, "test": {}, "update": {}},
		"yarn": {"add": {}, "build": {}, "install": {}, "lint": {}, "run": {}, "test": {}, "upgrade": {}},
		"bun":  {"add": {}, "build": {}, "install": {}, "run": {}, "test": {}, "update": {}},
	}
	if _, ok := allowed[program][subcommand]; ok {
		return subcommand
	}
	return "other"
}

func isShellProgram(program string) bool {
	switch program {
	case "sh", "dash", "bash", "zsh", "ksh":
		return true
	default:
		return false
	}
}

func hasStaticArgument(args []*syntax.Word, want string) bool {
	for _, arg := range args {
		if value, ok := safeStaticWord(arg); ok && value == want {
			return true
		}
	}
	return false
}

func hasForceArgument(args []*syntax.Word) bool {
	for _, arg := range args {
		if value, ok := safeStaticWord(arg); ok && (value == "-f" || strings.HasPrefix(value, "-f") || value == "--force") {
			return true
		}
	}
	return false
}

func hasRecursiveArgument(args []*syntax.Word) bool {
	for _, arg := range args {
		value, ok := safeStaticWord(arg)
		if !ok {
			continue
		}
		if value == "--" {
			return false
		}
		if value == "--recursive" || value == "-R" || value == "-r" {
			return true
		}
		if strings.HasPrefix(value, "-") && !strings.HasPrefix(value, "--") && strings.ContainsAny(value[1:], "Rr") {
			return true
		}
	}
	return false
}

func truncatesRedirect(redirect *syntax.Redirect) bool {
	switch redirect.Op.String() {
	case ">", ">|":
	default:
		return false
	}
	// Writing to a discard sink destroys nothing: there is no prior content to
	// lose. `2>/dev/null` and its cmd.exe spelling `2>NUL` are how a command
	// silences a probe, so classifying them as file-truncate hard-blocks ordinary
	// read-only work. A target that cannot be resolved statically stays dangerous.
	if target, ok := safeStaticWord(redirect.Word); ok && isDiscardRedirectTarget(target) {
		return false
	}
	return true
}

// isDiscardRedirectTarget reports whether a redirection target is a sink that
// holds no content of its own, so truncating it cannot lose data. It covers the
// POSIX null device, the cmd.exe spellings of the same device, and the standard
// stream aliases.
func isDiscardRedirectTarget(target string) bool {
	normalized := strings.ToLower(strings.TrimSpace(target))
	normalized = strings.Trim(normalized, `"'`)
	if normalized == "" {
		return false
	}
	// cmd.exe accepts `\\.\NUL` and a trailing colon, and tolerates either slash.
	normalized = strings.ReplaceAll(normalized, `\`, "/")
	normalized = strings.TrimPrefix(normalized, "//./")
	normalized = strings.TrimSuffix(normalized, ":")
	switch normalized {
	case "nul", "/dev/null", "dev/null", "con", "/dev/tty",
		"/dev/stdout", "/dev/stderr", "/dev/fd/1", "/dev/fd/2":
		return true
	default:
		return false
	}
}

func sortedFactLabels(labels map[string]struct{}) []string {
	if len(labels) == 0 {
		return nil
	}
	out := make([]string, 0, len(labels))
	for label := range labels {
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

func mergeFactLabels(left, right []string) []string {
	labels := make(map[string]struct{}, len(left)+len(right))
	for _, label := range left {
		labels[label] = struct{}{}
	}
	for _, label := range right {
		labels[label] = struct{}{}
	}
	return sortedFactLabels(labels)
}

func commandDangerWarningFromFacts(facts CommandFacts) string {
	for _, dangerous := range facts.Dangerous {
		if warning := commandDangerWarning(dangerous); warning != "" {
			return warning
		}
	}
	return ""
}

// CommandRiskExplanation is the structured, localizable description of one risk
// label. Centralizing it keeps the reason, the consequence, and the suggested
// safe alternative together instead of scattering prose across the codebase,
// and gives the UI a stable key to translate.
type CommandRiskExplanation struct {
	Label       string `json:"label"`
	MessageKey  string `json:"messageKey"`
	Summary     string `json:"summary"`
	Why         string `json:"why"`
	Alternative string `json:"alternative,omitempty"`
	Hard        bool   `json:"hard"`
}

// commandRiskExplanations is the single source of truth for every risk label
// produced by either the POSIX or the Windows analyzer. Hard entries are
// blocked outright; the rest always require human approval.
var commandRiskExplanations = map[string]CommandRiskExplanation{
	// Hard-blocked: catastrophic and effectively irreversible.
	"file-delete":           {MessageKey: "danger.fileDelete", Summary: "Recursive or forced delete permanently removes files or directories.", Why: "Deleted content is not recoverable from the working tree.", Alternative: "Move the target to a temporary directory, or delete a specific path after review.", Hard: true},
	"file-destroy":          {MessageKey: "danger.fileDestroy", Summary: "This command overwrites file contents so they cannot be recovered.", Why: "Overwritten data cannot be restored even with undelete tooling.", Hard: true},
	"file-truncate":         {MessageKey: "danger.fileTruncate", Summary: "This truncates an existing file to zero length.", Why: "The previous contents are lost immediately.", Alternative: "Append with >> or write through the Write tool so the change is reviewable.", Hard: true},
	"find-delete":           {MessageKey: "danger.findDelete", Summary: "find -delete removes matching files in bulk.", Why: "A wrong pattern can delete far more than intended.", Alternative: "Run the same find without -delete first and inspect the matches.", Hard: true},
	"privilege-escalation":  {MessageKey: "danger.privilegeEscalation", Summary: "This runs a command with elevated privileges.", Why: "Elevated commands can modify the whole system, not just this workspace.", Hard: true},
	"disk-write":            {MessageKey: "danger.diskWrite", Summary: "This writes directly to a block device.", Why: "Raw device writes destroy filesystems and are not recoverable.", Hard: true},
	"disk-format":           {MessageKey: "danger.diskFormat", Summary: "This formats a volume.", Why: "Formatting erases every file on the target volume.", Hard: true},
	"disk-partition":        {MessageKey: "danger.diskPartition", Summary: "This rewrites partition or filesystem metadata.", Why: "Partition changes can make an entire disk unreadable.", Hard: true},
	"network-pipe-shell":    {MessageKey: "danger.networkPipeShell", Summary: "This pipes downloaded content into an interpreter.", Why: "Remote code runs immediately with your privileges and is never reviewed.", Alternative: "Download to a file, inspect it, then run it as a separate approved step.", Hard: true},
	"decoded-pipe-shell":    {MessageKey: "danger.decodedPipeShell", Summary: "This decodes content and pipes it into an interpreter.", Why: "The decoded payload is hidden from review and executes immediately.", Alternative: "Decode to a file and inspect it before running.", Hard: true},
	"permission-weaken":     {MessageKey: "danger.permissionWeaken", Summary: "This recursively weakens access permissions.", Why: "Broadly writable paths let any local process modify your files.", Alternative: "Apply the narrowest permission to a specific path instead.", Hard: true},
	"permission-setuid":     {MessageKey: "danger.permissionSetuid", Summary: "This sets the setuid or setgid bit.", Why: "A setuid binary runs with the owner's privileges for every user.", Hard: true},
	"git-clean":             {MessageKey: "danger.gitClean", Summary: "git clean -f deletes untracked files.", Why: "Untracked files are not in history and cannot be recovered.", Alternative: "Run git clean -n first to preview exactly what would be removed.", Hard: true},
	"git-reset-hard":        {MessageKey: "danger.gitResetHard", Summary: "git reset --hard discards local changes.", Why: "Uncommitted work is lost with no reflog entry.", Alternative: "git stash keeps the same clean state but is reversible.", Hard: true},
	"process-kill-all":      {MessageKey: "danger.processKillAll", Summary: "This terminates every matching process.", Why: "It can kill your session, the IDE, and unsaved work.", Hard: true},
	"crontab-delete":        {MessageKey: "danger.crontabDelete", Summary: "crontab -r removes all scheduled jobs for the user.", Why: "The crontab is deleted with no confirmation and no backup.", Alternative: "Back up with crontab -l first.", Hard: true},
	"firewall-flush":        {MessageKey: "danger.firewallFlush", Summary: "This flushes firewall rules.", Why: "The host is left unprotected until rules are restored.", Hard: true},
	"container-delete":      {MessageKey: "danger.containerDelete", Summary: "This force-removes containers, images, or volumes.", Why: "Volume data is deleted permanently.", Alternative: "Prune a specific resource after listing what would be removed.", Hard: true},
	"system-shutdown":       {MessageKey: "danger.systemShutdown", Summary: "This shuts down or restarts the machine.", Why: "All unsaved work in every application is lost.", Hard: true},
	"shadow-copy-delete":    {MessageKey: "danger.shadowCopyDelete", Summary: "This deletes Windows shadow copies or backups.", Why: "It removes the system restore points needed to recover from data loss, and is the canonical ransomware precursor.", Hard: true},
	"registry-delete":       {MessageKey: "danger.registryDelete", Summary: "This deletes registry keys.", Why: "Removing a hive key can break Windows or installed software irreparably.", Hard: true},
	"boot-config":           {MessageKey: "danger.bootConfig", Summary: "This modifies boot configuration.", Why: "An incorrect boot entry can make Windows unbootable.", Hard: true},
	"service-delete":        {MessageKey: "danger.serviceDelete", Summary: "This deletes a system service.", Why: "Removing a service can disable system functionality permanently.", Hard: true},
	"account-change":        {MessageKey: "danger.accountChange", Summary: "This creates or deletes a user account or group membership.", Why: "Account changes alter who can log in to this machine.", Hard: true},
	"scheduled-task-change": {MessageKey: "danger.scheduledTaskChange", Summary: "This creates, changes, or deletes a scheduled task.", Why: "Scheduled tasks persist and run outside this session.", Hard: true},
	"audit-clear":           {MessageKey: "danger.auditClear", Summary: "This clears event or audit logs.", Why: "Clearing logs destroys the record needed to investigate incidents.", Hard: true},
	"encoded-command":       {MessageKey: "danger.encodedCommand", Summary: "This runs a base64-encoded command payload.", Why: "The real command cannot be reviewed before it executes.", Alternative: "Provide the command in plain text so it can be inspected.", Hard: true},
	"network-download-exec": {MessageKey: "danger.networkDownloadExec", Summary: "This downloads content with a tool commonly used to stage payloads.", Why: "Downloaded code can execute without review.", Hard: true},
	"script-host-execution": {MessageKey: "danger.scriptHostExecution", Summary: "This launches a Windows script or DLL host.", Why: "These hosts execute arbitrary code and are a common malware vector.", Hard: true},
	"policy-weaken":         {MessageKey: "danger.policyWeaken", Summary: "This weakens the PowerShell execution policy.", Why: "It allows unsigned scripts to run for future sessions too.", Hard: true},
	"firewall-change":       {MessageKey: "danger.firewallChange", Summary: "This reconfigures the Windows firewall.", Why: "Firewall changes can expose this machine to the network.", Hard: true},

	// Approval-required: serious but recoverable, and legitimate in normal work.
	"file-delete-scoped":     {MessageKey: "sensitive.fileDeleteScoped", Summary: "This deletes a specific file or directory.", Why: "The target is removed from disk.", Alternative: "Confirm the path is what you expect before allowing."},
	"file-truncate-scoped":   {MessageKey: "sensitive.fileTruncateScoped", Summary: "This resizes a file in place.", Why: "Content beyond the new size is discarded."},
	"file-overwrite":         {MessageKey: "sensitive.fileOverwrite", Summary: "This overwrites a file's contents.", Why: "The previous contents are replaced."},
	"permission-change":      {MessageKey: "sensitive.permissionChange", Summary: "This changes file ownership or permissions.", Why: "Access control for the target changes."},
	"process-kill":           {MessageKey: "sensitive.processKill", Summary: "This terminates a process.", Why: "The target process loses unsaved state."},
	"service-change":         {MessageKey: "sensitive.serviceChange", Summary: "This starts, stops, or reconfigures a service.", Why: "System or project services may become unavailable."},
	"package-install":        {MessageKey: "sensitive.packageInstall", Summary: "This installs packages, which runs their install scripts.", Why: "Package install hooks execute arbitrary code with your privileges.", Alternative: "Review the lockfile diff, or install with scripts disabled."},
	"git-force-push":         {MessageKey: "sensitive.gitForcePush", Summary: "This force-pushes and overwrites remote history.", Why: "Commits on the remote branch can be lost for everyone.", Alternative: "--force-with-lease refuses to clobber work you have not seen."},
	"git-history-rewrite":    {MessageKey: "sensitive.gitHistoryRewrite", Summary: "This rewrites repository history.", Why: "Every existing commit hash changes and clones diverge."},
	"git-discard-changes":    {MessageKey: "sensitive.gitDiscardChanges", Summary: "This discards local file changes.", Why: "Uncommitted edits to the affected paths are lost."},
	"registry-write":         {MessageKey: "sensitive.registryWrite", Summary: "This modifies the Windows registry.", Why: "Registry changes affect system and application behavior."},
	"network-config":         {MessageKey: "sensitive.networkConfig", Summary: "This changes network configuration.", Why: "Connectivity or firewall posture may change."},
	"network-download":       {MessageKey: "sensitive.networkDownload", Summary: "This downloads content from the network.", Why: "Downloaded content is untrusted until reviewed."},
	"network-share":          {MessageKey: "sensitive.networkShare", Summary: "This changes network file sharing.", Why: "Shares can expose local files to other machines."},
	"pipe-to-interpreter":    {MessageKey: "sensitive.pipeToInterpreter", Summary: "This pipes data into an interpreter.", Why: "Whatever the left side produces will be executed as code."},
	"filesystem-mount":       {MessageKey: "sensitive.filesystemMount", Summary: "This mounts or unmounts a filesystem.", Why: "Mount changes can hide or expose data."},
	"scheduled-task-inspect": {MessageKey: "sensitive.scheduledTaskInspect", Summary: "This reads or edits scheduled tasks.", Why: "Scheduled tasks run outside this session."},
	"account-inspect":        {MessageKey: "sensitive.accountInspect", Summary: "This reads or modifies local account state.", Why: "Account state controls who can log in to this machine."},
	"process-spawn":          {MessageKey: "sensitive.processSpawn", Summary: "This starts a detached process.", Why: "A detached process escapes this run's lifecycle and cancellation."},
	"obfuscation":            {MessageKey: "sensitive.obfuscation", Summary: "This encodes or decodes content inline.", Why: "Encoding hides the real payload from review."},
	"bulk-copy":              {MessageKey: "sensitive.bulkCopy", Summary: "This copies files in bulk.", Why: "Bulk copies can overwrite existing files at the destination."},
	"file-rename":            {MessageKey: "sensitive.fileRename", Summary: "This renames files.", Why: "Renaming can overwrite an existing target."},
	"script-file-execution":  {MessageKey: "sensitive.scriptFileExecution", Summary: "This executes a script file.", Why: "The script's contents are not reviewed here."},
	"system-management":      {MessageKey: "sensitive.systemManagement", Summary: "This invokes a system management interface.", Why: "It can change machine-wide state."},
	"disk-tooling":           {MessageKey: "sensitive.diskTooling", Summary: "This runs a low-level disk utility.", Why: "Disk utilities can alter filesystem metadata."},
	"backup-tooling":         {MessageKey: "sensitive.backupTooling", Summary: "This runs a backup utility.", Why: "Backup operations can overwrite existing recovery points."},
}

// CommandRiskExplanationFor returns the structured explanation for a label.
func CommandRiskExplanationFor(label string) (CommandRiskExplanation, bool) {
	explanation, ok := commandRiskExplanations[label]
	if !ok {
		return CommandRiskExplanation{}, false
	}
	explanation.Label = label
	return explanation, true
}

func commandDangerWarning(dangerous string) string {
	explanation, ok := commandRiskExplanations[dangerous]
	if !ok || !explanation.Hard {
		return ""
	}
	warning := explanation.Summary + " " + explanation.Why
	if explanation.Alternative != "" {
		warning += " " + explanation.Alternative
	}
	return warning
}

// commandSensitiveWarning describes an approval-required label.
func commandSensitiveWarning(label string) string {
	explanation, ok := commandRiskExplanations[label]
	if !ok || explanation.Hard {
		return ""
	}
	warning := explanation.Summary + " " + explanation.Why
	if explanation.Alternative != "" {
		warning += " " + explanation.Alternative
	}
	return warning
}

// CommandApprovalWarning returns the best available human-facing reason a
// command needs review, preferring hard-block reasons over approval reasons.
func CommandApprovalWarning(facts CommandFacts) string {
	if warning := commandDangerWarningFromFacts(facts); warning != "" {
		return warning
	}
	for _, label := range facts.Sensitive {
		if warning := commandSensitiveWarning(label); warning != "" {
			return warning
		}
	}
	if facts.Unclassified() {
		return "This command could not be classified by static analysis, so it cannot run without review."
	}
	return ""
}
