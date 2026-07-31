package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// OpenURLTool hands a URL to the operating system's default handler.
//
// Doing this through Bash does not work and cannot be made to work: the shell
// is run inside a Job Object with KILL_ON_JOB_CLOSE so no process outlives a
// cancelled run, which is exactly the guarantee that kills a browser the shell
// just launched. This tool is deliberately outside that boundary -- it starts
// one short-lived launcher, never attaches pipes to it, and joins no job, so
// the browser it opens belongs to the user's session rather than to the run.
type OpenURLTool struct{}

const (
	openURLMaxLength    = 2048
	openURLLaunchGrace  = 5 * time.Second
	openURLWindowsShell = "rundll32"
)

type openURLInput struct {
	URL string `json:"url" desc:"Absolute http or https URL to open in the user's default browser."`
}

func (OpenURLTool) Name() string { return "OpenURL" }
func (OpenURLTool) Description() string {
	return "Open an http(s) URL in the user's default browser. Use this instead of a shell command such as `start` or `xdg-open`; those run inside the managed process group and the browser is terminated with the run."
}
func (OpenURLTool) Schema() any               { return openURLInput{} }
func (OpenURLTool) Risk(json.RawMessage) Risk { return RiskExec }

func (OpenURLTool) Execute(_ context.Context, call Call, _ Env) (Result, error) {
	var input openURLInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	target, err := validateOpenURL(input.URL)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	name, args := openURLLaunchArgs(runtime.GOOS, target)
	// No context: the launcher must not be cancelled halfway through handing the
	// URL over, and it exits on its own within milliseconds either way.
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return Result{Output: fmt.Sprintf("could not run %s to open the URL: %v", name, err), IsError: true}, nil
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case waitErr := <-done:
		if waitErr != nil {
			return Result{Output: fmt.Sprintf("%s refused to open %s: %v", name, target, waitErr), IsError: true}, nil
		}
	case <-time.After(openURLLaunchGrace):
		// Some desktop handlers stay alive as the browser's parent. There is
		// nothing further to learn by waiting, and the goroutine above still
		// reaps the process whenever it does exit.
	}
	return Result{
		Output: "opened " + target + " with the system default handler",
		Meta:   map[string]any{"url": target},
	}, nil
}

// openURLLaunchArgs is separate from Execute so the argv for every platform can
// be asserted in a test without launching a browser on the machine running it.
func openURLLaunchArgs(goos, target string) (string, []string) {
	switch goos {
	case "windows":
		// rundll32 passes the URL through verbatim. `cmd /c start` does not: it
		// reads & as a command separator and needs an empty title argument that
		// no quoting scheme survives intact.
		return openURLWindowsShell, []string{"url.dll,FileProtocolHandler", target}
	case "darwin":
		return "open", []string{target}
	default:
		return "xdg-open", []string{target}
	}
}

// validateOpenURL keeps the tool to the one capability it advertises. Handing an
// arbitrary scheme to the system handler is remote code execution by another
// name: file:// reads local disk, and schemes registered by installed
// applications launch those applications with attacker-chosen arguments.
func validateOpenURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("url is required")
	}
	if len(value) > openURLMaxLength {
		return "", fmt.Errorf("url is longer than the %d character maximum", openURLMaxLength)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("url contains a control character")
		}
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("url could not be parsed: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", errors.New("only http and https URLs can be opened; other schemes can read local files or start local applications")
	}
	if parsed.Host == "" {
		return "", errors.New("url must include a host")
	}
	// String() re-escapes the path and query, so whitespace cannot reach the
	// launcher's command line as a separate word.
	normalized := parsed.String()
	if strings.ContainsAny(normalized, " \t") {
		return "", errors.New("url contains unescaped whitespace")
	}
	return normalized, nil
}
