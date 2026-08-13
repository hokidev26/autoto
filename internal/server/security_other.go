//go:build !windows

package server

import "os"

// hardenTokenPermissions applies the POSIX owner-only modes for the secrets
// dir and token file, matching the chmod calls this replaced.
func hardenTokenPermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.Chmod(path, 0o700)
	}
	return os.Chmod(path, 0o600)
}
