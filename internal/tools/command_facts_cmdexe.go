package tools

import (
	"strings"
)

// This file implements the cmd.exe/PowerShell side of command danger analysis.
// BashTool executes `cmd /C <command>` on Windows, so the POSIX shell parser in
// command_facts.go cannot see the commands that actually run. Without this
// analyzer the entire danger tier is inert on Windows: native destructive verbs
// such as `del /s /q`, `rd /s /q`, `format`, `vssadmin delete shadows`, and
// `powershell -c "Remove-Item -Recurse -Force"` would classify as ordinary exec
// risk and bypass the hard block that stops their POSIX equivalents.
//
// Deliberately *not* named command_facts_windows.go. That filename is an
// implicit GOOS=windows build constraint, but analyzeBashCommand dispatches to
// this analyzer on `runtime.GOOS == "windows"` rather than at compile time, so
// the constrained spelling left the call undefined on every other platform and
// broke `GOOS=linux`/`GOOS=darwin` builds of the CLI outright. Nothing here
// touches a Windows API -- it is string analysis over a command line -- so it
// compiles everywhere, and building it everywhere is also what keeps the
// classifier's tests running on the Linux CI runner that gates this repo.
//
// Like the POSIX analyzer, the output is deliberately argument-free: only
// allowlisted category labels are recorded, never raw arguments, paths, or
// redirection targets.

const maxWindowsNestedAnalysisDepth = 2

// winToken is one scanned command-line word.
type winToken struct {
	text    string
	dynamic bool // contained %VAR% or !VAR! expansion
}

// winStatement is one command in a compound cmd.exe line.
type winStatement struct {
	tokens     []winToken
	pipedInto  bool // this statement receives another statement's output
	pipesForth bool // this statement pipes into the next one
}

type winScan struct {
	statements  []winStatement
	compound    bool
	pipeline    bool
	redirection bool
	// The costliest redirect target on the line, as a classifyRedirectTarget tier.
	// Held as a tier rather than a bool so a scratch log and a raw device are not
	// treated as the same act.
	truncationTier int
	substitution   bool
	unbalanced     bool
}

// analyzeWindowsCommand returns a conservative, JSON-safe summary of a cmd.exe
// command line. ParseKnown is false when the line contains constructs whose
// effect cannot be determined statically (unbalanced quotes, a variable-expanded
// program name, or a base64-encoded PowerShell payload), which callers treat as
// fail-closed rather than safe.
func analyzeWindowsCommand(command string) CommandFacts {
	return analyzeWindowsCommandDepth(command, 0)
}

func analyzeWindowsCommandDepth(command string, depth int) CommandFacts {
	var facts CommandFacts
	if strings.TrimSpace(command) == "" {
		return facts
	}
	scan := scanWindowsCommand(command)
	facts.ParseKnown = !scan.unbalanced
	facts.Compound = scan.compound
	facts.Pipeline = scan.pipeline
	facts.Redirection = scan.redirection
	facts.Substitution = scan.substitution

	collector := &windowsFactsCollector{
		facts:     &facts,
		effects:   make(map[string]struct{}),
		dangerous: make(map[string]struct{}),
		sensitive: make(map[string]struct{}),
		depth:     depth,
	}
	switch scan.truncationTier {
	case redirectTargetDevice:
		collector.effect("filesystem-write")
		collector.danger("file-truncate")
	case redirectTargetFile:
		// Unchanged on purpose; see the matching branch in command_facts.go.
		collector.effect("filesystem-write")
		collector.danger("file-truncate")
	case redirectTargetScratch:
		collector.effect("filesystem-write")
	}
	for index := range scan.statements {
		collector.visitStatement(scan.statements[index])
	}
	collector.finish()
	return facts
}

// scanWindowsCommand splits a cmd.exe line into statements while tracking the
// shell metacharacters that matter for risk. cmd.exe uses `^` as its escape
// character and `&` as a sequential separator (not POSIX backgrounding).
func scanWindowsCommand(command string) winScan {
	var scan winScan
	var tokens []winToken
	var current strings.Builder
	currentDynamic := false
	inQuote := false
	pendingPipe := false

	flushToken := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, winToken{text: current.String(), dynamic: currentDynamic})
		current.Reset()
		currentDynamic = false
	}
	flushStatement := func(pipesForth bool) {
		flushToken()
		if len(tokens) > 0 {
			scan.statements = append(scan.statements, winStatement{
				tokens:     tokens,
				pipedInto:  pendingPipe,
				pipesForth: pipesForth,
			})
			tokens = nil
		}
		pendingPipe = pipesForth
	}

	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		char := runes[i]
		if char == '^' && !inQuote && i+1 < len(runes) {
			i++
			current.WriteRune(runes[i])
			continue
		}
		if char == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			if char == '%' || char == '!' {
				currentDynamic = true
			}
			current.WriteRune(char)
			continue
		}
		switch char {
		case '%', '!':
			currentDynamic = true
			current.WriteRune(char)
		case '|':
			if i+1 < len(runes) && runes[i+1] == '|' {
				i++
				scan.compound = true
				flushStatement(false)
			} else {
				scan.compound = true
				scan.pipeline = true
				flushStatement(true)
			}
		case '&':
			if i+1 < len(runes) && runes[i+1] == '&' {
				i++
			}
			scan.compound = true
			flushStatement(false)
		case '>':
			scan.redirection = true
			// `>>` appends; a bare `>` may truncate, depending on the target.
			truncates := true
			if i+1 < len(runes) && runes[i+1] == '>' {
				i++
				truncates = false
			}
			flushToken()
			for i+1 < len(runes) && (runes[i+1] == ' ' || runes[i+1] == '\t') {
				i++
			}
			// `2>&1` duplicates a handle rather than touching a file. Consume the
			// `&N` here so the `&` is not mistaken for a statement separator, which
			// would also leave the digit behind as a bogus program name.
			if i+1 < len(runes) && runes[i+1] == '&' {
				i++
				for i+1 < len(runes) && runes[i+1] >= '0' && runes[i+1] <= '9' {
					i++
				}
				break
			}
			// Capture the redirection target so it never becomes a program name.
			// The text is used only to classify the sink; it is never recorded.
			// The stop set must match the token separators below: cmd.exe also
			// delimits on `;` and `,`, and leaving them in the target turned
			// `>NUL;` into the unknown file "NUL;", which hard-blocked a command
			// that destroys nothing.
			targetStart := i + 1
			for i+1 < len(runes) && !strings.ContainsRune(" \t|&<>,;\r\n", runes[i+1]) {
				i++
			}
			if truncates {
				if tier := classifyRedirectTarget(string(runes[targetStart : i+1])); tier > scan.truncationTier {
					scan.truncationTier = tier
				}
			}
		case '<':
			scan.redirection = true
			flushToken()
		case '(', ')':
			scan.compound = true
			flushToken()
		case ' ', '\t', ',', ';', '\r', '\n':
			flushToken()
		default:
			current.WriteRune(char)
		}
	}
	scan.unbalanced = inQuote
	flushStatement(false)
	if len(scan.statements) > 1 {
		scan.compound = true
	}
	return scan
}

type windowsFactsCollector struct {
	facts     *CommandFacts
	effects   map[string]struct{}
	dangerous map[string]struct{}
	sensitive map[string]struct{}
	depth     int
	// Whether an earlier stage of the pipeline currently being walked was a
	// network downloader. "curl x | sh" is remote code execution; "go test | sh"
	// is not, and only the upstream stage can tell them apart. Statements are
	// visited left to right, so this is set by the downloader and read by the
	// interpreter. Cleared at every pipeline boundary so a download in one
	// pipeline cannot taint the next one on the same line.
	pipedFromNetwork bool
}

func (c *windowsFactsCollector) effect(label string)         { c.effects[label] = struct{}{} }
func (c *windowsFactsCollector) danger(label string)         { c.dangerous[label] = struct{}{} }
func (c *windowsFactsCollector) sensitiveLabel(label string) { c.sensitive[label] = struct{}{} }

func (c *windowsFactsCollector) finish() {
	c.facts.Effects = sortedFactLabels(c.effects)
	c.facts.Dangerous = sortedFactLabels(c.dangerous)
	c.facts.Sensitive = sortedFactLabels(c.sensitive)
}

func (c *windowsFactsCollector) visitStatement(statement winStatement) {
	if len(statement.tokens) == 0 {
		return
	}
	c.facts.CommandCount++
	head := statement.tokens[0]
	if head.dynamic {
		// A variable-expanded program name cannot be classified statically.
		if c.facts.Program == "" {
			c.facts.Program = "dynamic"
		}
		c.facts.ParseKnown = false
		return
	}
	program := windowsProgramName(head.text)
	if c.facts.Program == "" {
		c.facts.Program = program
	}
	args := statement.tokens[1:]
	if c.facts.Subcommand == "" && isSubcommandProgram(program) && len(args) > 0 && !args[0].dynamic {
		c.facts.Subcommand = stableSubcommand(program, strings.ToLower(args[0].text))
	}
	c.classify(program, args, statement)
}

// windowsProgramName normalizes a program token to a bare, extension-free,
// lower-case name. Unrepresentable tokens collapse to "other" so callers can
// treat them as unclassified rather than silently safe.
func windowsProgramName(program string) string {
	program = strings.TrimSpace(program)
	if program == "" {
		return "other"
	}
	// Strip any directory prefix in either separator form.
	if index := strings.LastIndexAny(program, `\/`); index >= 0 {
		program = program[index+1:]
	}
	program = strings.ToLower(strings.Trim(program, `"'`))
	for _, extension := range []string{".exe", ".com", ".cmd", ".bat", ".ps1"} {
		if strings.HasSuffix(program, extension) {
			program = strings.TrimSuffix(program, extension)
			break
		}
	}
	if program == "" || len(program) > maxCommandFactProgramBytes {
		return "other"
	}
	if !safeProgramToken(program) {
		return "other"
	}
	return program
}

func winHasFlag(args []winToken, flags ...string) bool {
	for _, arg := range args {
		normalized := strings.ToLower(strings.TrimLeft(arg.text, "-/"))
		for _, flag := range flags {
			if normalized == flag {
				return true
			}
		}
	}
	return false
}

// winHasDiscardDeviceArgument reports whether any whole argument names the null
// device. As a copy source it blanks the destination, which is the truncate shape
// worth blocking.
func winHasDiscardDeviceArgument(args []winToken) bool {
	for _, arg := range args {
		if isDiscardRedirectTarget(arg.text) {
			return true
		}
	}
	return false
}

func winHasAnySubstring(args []winToken, needles ...string) bool {
	for _, arg := range args {
		lowered := strings.ToLower(arg.text)
		for _, needle := range needles {
			if strings.Contains(lowered, needle) {
				return true
			}
		}
	}
	return false
}

func winArgAt(args []winToken, index int) string {
	if index < 0 || index >= len(args) {
		return ""
	}
	return strings.ToLower(args[index].text)
}

// classify maps a Windows program plus its flags onto allowlisted risk labels.
// Hard-block labels ("dangerous") are reserved for catastrophic, effectively
// irreversible actions; recoverable-but-serious actions are recorded as
// "sensitive" so they always require human approval without being unapprovable.
func (c *windowsFactsCollector) classify(program string, args []winToken, statement winStatement) {
	switch program {
	case "del", "erase":
		c.effect("filesystem-delete")
		if winHasFlag(args, "s") || winHasAnySubstring(args, "*", "?") {
			c.danger("file-delete")
		} else {
			c.sensitiveLabel("file-delete-scoped")
		}
	// POSIX tooling stays reachable on Windows through Git for Windows, MSYS2,
	// and similar bundles, so the same verbs must classify here too.
	case "rm":
		c.effect("filesystem-delete")
		if winHasFlag(args, "r", "rf", "fr", "f", "recursive", "force") || winHasAnySubstring(args, "*") {
			c.danger("file-delete")
		} else {
			c.sensitiveLabel("file-delete-scoped")
		}
	case "unlink":
		c.effect("filesystem-delete")
		c.danger("file-delete")
	case "shred":
		c.effect("filesystem-delete")
		c.danger("file-destroy")
	case "dd":
		c.effect("disk-write")
		c.danger("disk-write")
	case "sudo":
		c.effect("privileged-execution")
		c.danger("privilege-escalation")
	case "rd", "rmdir":
		c.effect("filesystem-delete")
		if winHasFlag(args, "s") {
			c.danger("file-delete")
		} else {
			c.sensitiveLabel("file-delete-scoped")
		}
	case "format":
		c.effect("disk-format")
		c.danger("disk-format")
	case "diskpart":
		c.effect("disk-write")
		c.danger("disk-partition")
	case "fsutil":
		c.effect("disk-write")
		if winArgAt(args, 0) == "file" && winHasAnySubstring(args, "setzerodata") {
			c.danger("disk-write")
		} else {
			c.sensitiveLabel("disk-tooling")
		}
	case "vssadmin", "wbadmin":
		c.effect("backup-destroy")
		if winHasAnySubstring(args, "delete") {
			c.danger("shadow-copy-delete")
		} else {
			c.sensitiveLabel("backup-tooling")
		}
	case "wmic":
		if winHasAnySubstring(args, "shadowcopy") && winHasAnySubstring(args, "delete") {
			c.effect("backup-destroy")
			c.danger("shadow-copy-delete")
		} else if winHasAnySubstring(args, "call") || winHasAnySubstring(args, "delete") {
			c.effect("system-management")
			c.sensitiveLabel("system-management")
		}
	case "cipher":
		if winHasAnySubstring(args, "/w", "w:") {
			c.effect("filesystem-delete")
			c.danger("file-destroy")
		}
	case "sdelete":
		c.effect("filesystem-delete")
		c.danger("file-destroy")
	case "reg":
		if winArgAt(args, 0) == "delete" {
			c.effect("registry-write")
			c.danger("registry-delete")
		} else if winArgAt(args, 0) == "add" || winArgAt(args, 0) == "import" {
			c.effect("registry-write")
			c.sensitiveLabel("registry-write")
		}
	case "regedit":
		c.effect("registry-write")
		c.sensitiveLabel("registry-write")
	case "bcdedit":
		c.effect("boot-config")
		c.danger("boot-config")
	case "bootrec", "bootsect":
		c.effect("boot-config")
		c.danger("boot-config")
	case "takeown":
		c.effect("permission-change")
		if winHasFlag(args, "r") {
			c.danger("permission-weaken")
		} else {
			c.sensitiveLabel("permission-change")
		}
	case "icacls", "cacls", "xcacls":
		c.effect("permission-change")
		if winHasAnySubstring(args, "everyone", "authenticated users", "/reset") && winHasFlag(args, "t") {
			c.danger("permission-weaken")
		} else {
			c.sensitiveLabel("permission-change")
		}
	case "attrib":
		c.effect("permission-change")
		c.sensitiveLabel("permission-change")
	case "sc":
		if winArgAt(args, 0) == "delete" {
			c.effect("service-change")
			c.danger("service-delete")
		} else if winArgAt(args, 0) == "stop" || winArgAt(args, 0) == "config" || winArgAt(args, 0) == "create" {
			c.effect("service-change")
			c.sensitiveLabel("service-change")
		}
	case "net", "net1":
		switch winArgAt(args, 0) {
		case "user", "localgroup", "group":
			c.effect("account-change")
			if winHasFlag(args, "delete", "add") {
				c.danger("account-change")
			} else {
				c.sensitiveLabel("account-inspect")
			}
		case "stop", "start":
			c.effect("service-change")
			c.sensitiveLabel("service-change")
		case "share":
			c.effect("network-share")
			c.sensitiveLabel("network-share")
		}
	case "schtasks":
		c.effect("scheduled-task-change")
		if winHasFlag(args, "create", "delete", "change") {
			c.danger("scheduled-task-change")
		} else {
			c.sensitiveLabel("scheduled-task-inspect")
		}
	case "at":
		c.effect("scheduled-task-change")
		c.sensitiveLabel("scheduled-task-inspect")
	case "taskkill":
		c.effect("process-kill")
		if winHasFlag(args, "f") {
			c.sensitiveLabel("process-kill")
		}
	case "shutdown":
		c.effect("system-shutdown")
		c.danger("system-shutdown")
	case "wevtutil":
		if winArgAt(args, 0) == "cl" || winHasAnySubstring(args, "clear-log") {
			c.effect("audit-clear")
			c.danger("audit-clear")
		}
	case "netsh":
		c.effect("network-config")
		if winHasAnySubstring(args, "firewall", "advfirewall") {
			c.danger("firewall-change")
		} else {
			c.sensitiveLabel("network-config")
		}
	case "certutil":
		// certutil is a common living-off-the-land download/decode primitive.
		if winHasAnySubstring(args, "urlcache", "verifyctl") {
			c.effect("network-access")
			c.danger("network-download-exec")
		} else if winHasAnySubstring(args, "decode", "encode") {
			c.effect("obfuscation")
			c.sensitiveLabel("obfuscation")
		}
	case "bitsadmin":
		c.effect("network-access")
		c.danger("network-download-exec")
	case "curl", "wget":
		c.effect("network-access")
		if statement.pipesForth {
			c.sensitiveLabel("network-download")
		}
	case "start":
		c.facts.Background = true
		c.effect("process-spawn")
		c.sensitiveLabel("process-spawn")
	case "runas":
		c.effect("privileged-execution")
		c.danger("privilege-escalation")
	case "powershell", "pwsh", "powershell_ise":
		c.visitPowerShell(args)
	case "cmd":
		c.visitNestedCmd(args)
	case "mshta", "rundll32", "regsvr32", "wscript", "cscript", "installutil", "msbuild":
		// Script/DLL host binaries commonly used to run untrusted payloads.
		c.effect("shell-execution")
		c.danger("script-host-execution")
	case "copy", "move", "xcopy", "robocopy":
		c.effect("filesystem-write")
		// `copy nul target` blanks the target. Matched as a whole argument rather
		// than a substring so ordinary names such as `annul.txt` are not caught.
		if winHasDiscardDeviceArgument(args) {
			c.danger("file-truncate")
		}
		if program == "robocopy" && winHasFlag(args, "mir", "purge") {
			c.danger("file-delete")
		}
		if program == "xcopy" && winHasAnySubstring(args, "*") {
			c.sensitiveLabel("bulk-copy")
		}
	case "ren", "rename":
		c.effect("filesystem-write")
		c.sensitiveLabel("file-rename")
	case "git":
		c.classifyGit(args)
	case "npm", "pnpm", "yarn", "bun", "pip", "pip3":
		// Installs run arbitrary lifecycle scripts, but they are routine in a dev
		// IDE, so they require approval rather than a hard block.
		if winHasAnySubstring(args, "install", "add", "ci") {
			c.effect("package-install")
			c.sensitiveLabel("package-install")
		}
	case "docker", "podman":
		if winHasAnySubstring(args, "prune") || (winHasAnySubstring(args, "rm", "rmi") && winHasFlag(args, "f", "force")) {
			c.effect("container-delete")
			c.danger("container-delete")
		}
	case "other":
		c.facts.ParseKnown = false
	}

	// A downloader piped into any interpreter is remote code execution.
	if statement.pipesForth && (program == "curl" || program == "wget" || program == "certutil" || program == "bitsadmin") {
		c.effect("network-access")
		c.pipedFromNetwork = true
	}
	if statement.pipedInto && windowsInterpreter(program) {
		c.effect("shell-execution")
		// Only a download reaching the interpreter is remote code execution, which
		// is what this label means and what the POSIX classifier requires before
		// applying it. Piping a local program's output into PowerShell to format it
		// is ordinary shell work: it stays approvable, matching the POSIX
		// "pipe-to-interpreter" fallback, so danger reflection and human approval
		// can still stop a genuinely bad one.
		if c.pipedFromNetwork {
			c.danger("network-pipe-shell")
		} else {
			c.sensitiveLabel("pipe-to-interpreter")
		}
	}
	// A pipeline ends here, so no later stage inherits this one's network state.
	if !statement.pipesForth {
		c.pipedFromNetwork = false
	}
}

func (c *windowsFactsCollector) classifyGit(args []winToken) {
	subcommand := winArgAt(args, 0)
	switch subcommand {
	case "clean":
		if winHasFlag(args, "f", "fd", "fdx", "force") || winHasAnySubstring(args, "-f") {
			c.effect("filesystem-delete")
			c.danger("git-clean")
		}
	case "reset":
		if winHasAnySubstring(args, "--hard") {
			c.effect("repository-state-discard")
			c.danger("git-reset-hard")
		}
	case "push":
		if winHasAnySubstring(args, "--force", "-f", "+refs") && !winHasAnySubstring(args, "--force-with-lease") {
			c.effect("repository-history-rewrite")
			c.sensitiveLabel("git-force-push")
		}
	case "filter-branch", "filter-repo":
		c.effect("repository-history-rewrite")
		c.sensitiveLabel("git-history-rewrite")
	case "checkout", "restore":
		if winHasAnySubstring(args, "--force", "-f", ".") {
			c.effect("repository-state-discard")
			c.sensitiveLabel("git-discard-changes")
		}
	}
}

// windowsInterpreter reports whether a program executes code read from stdin or
// an argument. It intentionally includes non-shell interpreters, because piping
// a download into python or node is the same remote-code-execution shape as
// piping it into a shell.
func windowsInterpreter(program string) bool {
	switch program {
	case "cmd", "powershell", "pwsh", "powershell_ise",
		"sh", "bash", "zsh", "ksh", "dash",
		"python", "python2", "python3", "py",
		"perl", "ruby", "node", "deno", "bun",
		"php", "lua", "groovy", "osascript",
		"mshta", "wscript", "cscript", "rundll32", "regsvr32":
		return true
	default:
		return false
	}
}

func (c *windowsFactsCollector) visitNestedCmd(args []winToken) {
	if c.depth >= maxWindowsNestedAnalysisDepth {
		c.facts.ParseKnown = false
		return
	}
	for index, arg := range args {
		flag := strings.ToLower(strings.TrimLeft(arg.text, "-/"))
		if flag != "c" && flag != "k" {
			continue
		}
		if index+1 >= len(args) {
			c.facts.ParseKnown = false
			return
		}
		script := joinWinTokens(args[index+1:])
		c.effect("nested-shell")
		c.facts.Compound = true
		c.mergeNested(analyzeWindowsCommandDepth(script, c.depth+1))
		return
	}
}

func (c *windowsFactsCollector) visitPowerShell(args []winToken) {
	c.effect("shell-execution")
	for index, arg := range args {
		flag := strings.ToLower(strings.TrimLeft(arg.text, "-/"))
		switch flag {
		case "encodedcommand", "enc", "ec", "e":
			// A base64 payload cannot be classified; fail closed.
			c.facts.ParseKnown = false
			c.effect("obfuscation")
			c.danger("encoded-command")
			return
		case "command", "c":
			if index+1 >= len(args) {
				c.facts.ParseKnown = false
				return
			}
			c.analyzePowerShellScript(joinWinTokens(args[index+1:]))
			return
		case "file", "f":
			c.sensitiveLabel("script-file-execution")
			return
		}
	}
	// `powershell` with no recognizable script flag still runs a shell.
	c.analyzePowerShellScript(joinWinTokens(args))
}

func joinWinTokens(tokens []winToken) string {
	parts := make([]string, 0, len(tokens))
	for _, token := range tokens {
		parts = append(parts, token.text)
	}
	return strings.Join(parts, " ")
}

// powerShellDangerRule maps a lower-cased PowerShell pattern onto a risk label.
// Matching is substring-based because PowerShell's grammar (aliases, splatting,
// pipelines, arbitrary casing) cannot be parsed reliably here; the analyzer errs
// toward flagging and lets the approval gate resolve the rest.
type powerShellRule struct {
	needles []string // all must be present
	label   string
	effect  string
	hard    bool
}

var powerShellRules = []powerShellRule{
	{needles: []string{"remove-item", "-recurse"}, label: "file-delete", effect: "filesystem-delete", hard: true},
	{needles: []string{"remove-item", "-force"}, label: "file-delete", effect: "filesystem-delete", hard: true},
	{needles: []string{"ri ", "-recurse"}, label: "file-delete", effect: "filesystem-delete", hard: true},
	{needles: []string{"format-volume"}, label: "disk-format", effect: "disk-format", hard: true},
	{needles: []string{"clear-disk"}, label: "disk-format", effect: "disk-format", hard: true},
	{needles: []string{"initialize-disk"}, label: "disk-partition", effect: "disk-write", hard: true},
	{needles: []string{"remove-partition"}, label: "disk-partition", effect: "disk-write", hard: true},
	{needles: []string{"clear-content"}, label: "file-truncate", effect: "filesystem-write", hard: true},
	{needles: []string{"set-executionpolicy"}, label: "policy-weaken", effect: "policy-change", hard: true},
	{needles: []string{"invoke-expression"}, label: "network-pipe-shell", effect: "shell-execution", hard: true},
	{needles: []string{"iex"}, label: "network-pipe-shell", effect: "shell-execution", hard: true},
	{needles: []string{"downloadstring"}, label: "network-download-exec", effect: "network-access", hard: true},
	{needles: []string{"downloadfile"}, label: "network-download-exec", effect: "network-access", hard: true},
	{needles: []string{"frombase64string"}, label: "encoded-command", effect: "obfuscation", hard: true},
	{needles: []string{"stop-computer"}, label: "system-shutdown", effect: "system-shutdown", hard: true},
	{needles: []string{"restart-computer"}, label: "system-shutdown", effect: "system-shutdown", hard: true},
	{needles: []string{"remove-localuser"}, label: "account-change", effect: "account-change", hard: true},
	{needles: []string{"new-localuser"}, label: "account-change", effect: "account-change", hard: true},
	{needles: []string{"add-localgroupmember"}, label: "account-change", effect: "account-change", hard: true},
	{needles: []string{"unregister-scheduledtask"}, label: "scheduled-task-change", effect: "scheduled-task-change", hard: true},
	{needles: []string{"register-scheduledtask"}, label: "scheduled-task-change", effect: "scheduled-task-change", hard: true},
	{needles: []string{"remove-service"}, label: "service-delete", effect: "service-change", hard: true},
	{needles: []string{"clear-eventlog"}, label: "audit-clear", effect: "audit-clear", hard: true},
	{needles: []string{"start-process", "runas"}, label: "privilege-escalation", effect: "privileged-execution", hard: true},

	{needles: []string{"remove-item"}, label: "file-delete-scoped", effect: "filesystem-delete"},
	{needles: []string{"remove-itemproperty"}, label: "registry-write", effect: "registry-write"},
	{needles: []string{"set-itemproperty"}, label: "registry-write", effect: "registry-write"},
	{needles: []string{"new-service"}, label: "service-change", effect: "service-change"},
	{needles: []string{"stop-service"}, label: "service-change", effect: "service-change"},
	{needles: []string{"set-acl"}, label: "permission-change", effect: "permission-change"},
	{needles: []string{"invoke-webrequest"}, label: "network-download", effect: "network-access"},
	{needles: []string{"invoke-restmethod"}, label: "network-download", effect: "network-access"},
	{needles: []string{"set-content"}, label: "file-overwrite", effect: "filesystem-write"},
	{needles: []string{"out-file"}, label: "file-overwrite", effect: "filesystem-write"},
}

func (c *windowsFactsCollector) analyzePowerShellScript(script string) {
	lowered := strings.ToLower(script)
	if strings.TrimSpace(lowered) == "" {
		return
	}
	for _, rule := range powerShellRules {
		matched := true
		for _, needle := range rule.needles {
			if !strings.Contains(lowered, needle) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		if rule.effect != "" {
			c.effect(rule.effect)
		}
		if rule.hard {
			c.danger(rule.label)
		} else {
			c.sensitiveLabel(rule.label)
		}
	}
	// A download piped or chained into an evaluator is remote code execution.
	if (strings.Contains(lowered, "invoke-webrequest") || strings.Contains(lowered, "invoke-restmethod") ||
		strings.Contains(lowered, "iwr") || strings.Contains(lowered, "irm") || strings.Contains(lowered, "curl")) &&
		(strings.Contains(lowered, "iex") || strings.Contains(lowered, "invoke-expression")) {
		c.effect("shell-execution")
		c.danger("network-pipe-shell")
	}
}

func (c *windowsFactsCollector) mergeNested(nested CommandFacts) {
	if !nested.ParseKnown {
		c.facts.ParseKnown = false
	}
	c.facts.CommandCount += nested.CommandCount
	c.facts.Compound = c.facts.Compound || nested.Compound
	c.facts.Pipeline = c.facts.Pipeline || nested.Pipeline
	c.facts.Redirection = c.facts.Redirection || nested.Redirection
	c.facts.Substitution = c.facts.Substitution || nested.Substitution
	c.facts.Background = c.facts.Background || nested.Background
	for _, label := range nested.Effects {
		c.effect(label)
	}
	for _, label := range nested.Dangerous {
		c.danger(label)
	}
	for _, label := range nested.Sensitive {
		c.sensitiveLabel(label)
	}
}
