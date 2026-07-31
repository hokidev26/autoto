//go:build !windows

package tools

import "os/exec"

// Only cmd.exe needs the raw command line rebuilt. `/bin/sh -c` receives the
// command as one argv entry, which the exec layer passes through untouched.
func useShellCommandLine(*exec.Cmd, string) {}
