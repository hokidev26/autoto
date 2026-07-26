//go:build windows

package peercontrol

import (
	"errors"
	"os"
)

func installIdentityFile(temporaryPath, targetPath string) (bool, error) {
	if info, err := os.Lstat(targetPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, errors.New("peer identity target path is unsafe")
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	// Windows rename is atomic and does not replace an existing destination.
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		if _, statErr := os.Lstat(targetPath); statErr == nil {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
