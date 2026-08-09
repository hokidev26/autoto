//go:build windows

package process

import (
	"os/exec"
	"syscall"
	"testing"
)

// Short helper commands (git plumbing, capability probes) do not need a process
// group, but they do flash a console window without CREATE_NO_WINDOW. Git runs
// on every checkpoint and status refresh, so that flashing is near-continuous
// while a task works.
func TestHideWindowSuppressesTheConsoleWindow(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "echo hi")
	HideWindow(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("HideWindow must set SysProcAttr")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("CREATE_NO_WINDOW missing, the command would flash a console: flags %#x", cmd.SysProcAttr.CreationFlags)
	}
}

// Unlike Prepare, this must not put the child in its own process group: these
// are short synchronous calls the caller waits on, not trees needing reaping.
func TestHideWindowDoesNotCreateAProcessGroup(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "echo hi")
	HideWindow(cmd)
	if cmd.SysProcAttr.CreationFlags&createNewProcessGroup != 0 {
		t.Fatalf("HideWindow must not set CREATE_NEW_PROCESS_GROUP: flags %#x", cmd.SysProcAttr.CreationFlags)
	}
}

// A caller may already have set SysProcAttr; merging rather than replacing keeps
// whatever it configured, including a CmdLine that controls what actually runs.
func TestHideWindowPreservesExistingProcessAttributes(t *testing.T) {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /S /C "echo hi"`}
	HideWindow(cmd)
	if cmd.SysProcAttr.CmdLine != `cmd.exe /S /C "echo hi"` {
		t.Fatalf("CmdLine was overwritten: %q", cmd.SysProcAttr.CmdLine)
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("CREATE_NO_WINDOW missing: flags %#x", cmd.SysProcAttr.CreationFlags)
	}
}

// Defensive: the helper is called from several packages, and a nil command must
// not take the process down.
func TestHideWindowIgnoresNil(t *testing.T) {
	HideWindow(nil)
}
