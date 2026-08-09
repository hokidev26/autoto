//go:build !windows

package process

import "os/exec"

// HideWindow is a no-op away from Windows: no other platform allocates a console
// window for a child process, so there is nothing to suppress.
func HideWindow(*exec.Cmd) {}
