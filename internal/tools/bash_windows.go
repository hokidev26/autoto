//go:build windows

package tools

import (
	"os/exec"
	"syscall"
)

// Go escapes every argument with C-runtime rules, where a double quote becomes
// \". cmd.exe does not understand that escape, so a command as ordinary as
// `start "" "https://example.com"` reaches the shell as `start \"\" \"https://
// example.com\"`, which asks Windows to open a file named \\ -- a modal error
// dialog that blocks the shell until somebody clicks it, i.e. until the tool
// times out. cmd.exe /S has the opposite contract: strip exactly the outer
// quotes and run the rest verbatim, which is the only way to hand the shell the
// command that was actually written.
func useShellCommandLine(cmd *exec.Cmd, command string) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = `cmd.exe /S /C "` + command + `"`
}
