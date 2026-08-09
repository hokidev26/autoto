package agent

import "testing"

// The allowlist used to match on the subcommand alone, so `go test` and
// `git diff` were auto-approvable no matter what followed. Both programs accept
// flags that hand execution to another binary or write a file, which turned an
// allowlisted verb into an arbitrary-code or arbitrary-write primitive that
// skipped danger reflection AND, via canAutoExecuteTool, the approval prompt.
func TestWhitelistRejectsFlagsThatRedirectExecutionOrWriteFiles(t *testing.T) {
	escapes := []struct {
		command string
		why     string
	}{
		{`go test -exec "cmd /c echo pwned" ./...`, "-exec runs an arbitrary program instead of the test binary"},
		{`go test -exec=C:\evil.exe ./...`, "same escape in --flag=value form"},
		{`go test -toolexec "cmd /c echo pwned" ./...`, "-toolexec runs an arbitrary program per compiler invocation"},
		{`go build -toolexec "cmd /c echo pwned" ./...`, "-toolexec is not specific to go test"},
		{`go vet -vettool=C:\evil.exe ./...`, "-vettool runs an arbitrary analysis binary"},
		{`go build -ldflags "-X main.x=y" -o out ./...`, "linker flags are not part of the read-only fast path"},
		{`go test -gcflags=all=-N ./...`, "compiler flags are not part of the read-only fast path"},
		{`go build -overlay C:\overlay.json ./...`, "-overlay silently substitutes source files"},
		{`git diff --output=C:\pwned.txt`, "--output writes anywhere on disk"},
		{`git log --output=C:\pwned.txt`, "--output is accepted by log as well as diff"},
		{`git show --output=C:\pwned.txt`, "--output is accepted by show as well"},
		{`git diff --ext-diff`, "--ext-diff runs the repository's configured external differ"},
	}
	for _, escape := range escapes {
		if isWhitelistedExecCommand(escape.command) {
			t.Errorf("must not be whitelisted (%s): %s", escape.why, escape.command)
		}
	}
}

// The gate has to stay usable. Rejecting ordinary developer invocations would
// push people toward bypassPermissions, which is a worse outcome than the flags
// above.
func TestWhitelistStillAcceptsOrdinaryDeveloperInvocations(t *testing.T) {
	ordinary := []string{
		"go test ./...",
		"go test -v ./...",
		"go test -race -count=1 ./internal/agent/",
		"go test -run TestSomething -count=1 ./...",
		"go test -timeout 90s ./...",
		"go vet ./internal/...",
		"go build ./...",
		"go version",
		"git status",
		"git status --short",
		"git diff --stat",
		"git diff --cached --name-only",
		"git log --oneline",
		"git show --stat",
	}
	for _, command := range ordinary {
		if !isWhitelistedExecCommand(command) {
			t.Errorf("ordinary invocation should stay whitelisted: %s", command)
		}
	}
}

// An unknown flag must fail closed. Costing one model call is the right trade
// against predicting every flag Go and Git will ever add.
func TestWhitelistFailsClosedOnUnknownFlags(t *testing.T) {
	for _, command := range []string{
		"go test --some-future-flag ./...",
		"git diff --some-future-flag",
	} {
		if isWhitelistedExecCommand(command) {
			t.Errorf("unknown flag must fail closed: %s", command)
		}
	}
}

// operandsAreSafe must not mistake a path or package pattern for a flag, and
// must split --flag=value before matching.
func TestOperandsAreSafeSeparatesFlagsFromOperands(t *testing.T) {
	safe := map[string]struct{}{"--stat": {}, "-n": {}}
	cases := []struct {
		operands []string
		want     bool
	}{
		{operands: nil, want: true},
		{operands: []string{"./..."}, want: true},
		{operands: []string{"./internal/agent/", "TestFoo"}, want: true},
		{operands: []string{"--stat"}, want: true},
		{operands: []string{"-n", "5"}, want: true},
		{operands: []string{"--stat=always"}, want: true},
		{operands: []string{"--output=/tmp/x"}, want: false},
		{operands: []string{"./...", "--output=/tmp/x"}, want: false},
	}
	for _, test := range cases {
		if got := operandsAreSafe(test.operands, safe); got != test.want {
			t.Errorf("operandsAreSafe(%q) = %v, want %v", test.operands, got, test.want)
		}
	}
}
