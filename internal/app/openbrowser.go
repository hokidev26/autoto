package app

import (
	"os/exec"
	"runtime"
	"strings"
)

// openBrowser opens url in the user's default web browser. It is best-effort:
// the process is only launched (not waited on), and a launch failure is
// returned so the caller can log it without treating it as fatal.
func openBrowser(url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil
	}
	switch runtime.GOOS {
	case "windows":
		// rundll32 handles URLs with special characters (& ?) cleanly, unlike
		// `cmd /c start` which treats & as a command separator.
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// envTruthy reports whether an environment value should be read as "on".
func envTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
