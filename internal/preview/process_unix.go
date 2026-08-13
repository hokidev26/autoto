//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package preview

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

func dynamicSupported() bool { return true }

func validateLoopbackListener(ctx context.Context, port int) error {
	lsof, err := exec.LookPath("lsof")
	if err != nil {
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, 1500_000_000)
	defer cancel()
	output, runErr := exec.CommandContext(
		checkCtx,
		lsof,
		"-nP",
		"-iTCP:"+strconv.Itoa(port),
		"-sTCP:LISTEN",
		"-Fn",
	).Output()
	if runErr != nil && len(output) == 0 {
		return nil
	}
	for _, line := range strings.Split(string(output), "\n") {
		if !strings.HasPrefix(line, "n") {
			continue
		}
		listener := strings.ToLower(strings.TrimPrefix(line, "n"))
		if strings.HasPrefix(listener, "*:") || strings.HasPrefix(listener, "0.0.0.0:") || strings.HasPrefix(listener, "[::]:") || strings.HasPrefix(listener, ":::") {
			return errors.New("preview server exposed a wildcard listener")
		}
	}
	return nil
}
