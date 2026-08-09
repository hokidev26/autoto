//go:build windows

package process

import (
	"os/exec"
	"syscall"
)

// HideWindow suppresses the console window a child process would otherwise
// allocate, without the Job Object bookkeeping that Prepare sets up.
//
// Prepare is the right choice for a long-lived child whose whole tree has to be
// reaped. Short, synchronous helper commands -- git plumbing for checkpoints and
// status, capability probes -- do not need a process group, but they do still
// flash a console window on every invocation if nothing sets this flag. Git in
// particular runs on every checkpoint and status refresh, so the flashing is
// near-continuous while a task is working.
//
// Merges into any existing SysProcAttr rather than replacing it, so a caller
// that already set CmdLine keeps control of what actually runs.
func HideWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
