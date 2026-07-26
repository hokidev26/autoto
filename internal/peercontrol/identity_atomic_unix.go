//go:build !windows

package peercontrol

import (
	"errors"
	"os"
)

func installIdentityFile(temporaryPath, targetPath string) (bool, error) {
	if err := os.Link(temporaryPath, targetPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return true, err
	}
	return true, nil
}
