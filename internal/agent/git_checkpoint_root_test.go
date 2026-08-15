package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGitCheckpointRepoAllowedRejectsHomeAndVolumeRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if gitCheckpointRepoAllowed(home) {
		t.Fatalf("home directory %q must not be used as a git checkpoint root", home)
	}

	volumeRoot := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		volumeRoot = filepath.VolumeName(home) + string(filepath.Separator)
	}
	if gitCheckpointRepoAllowed(volumeRoot) {
		t.Fatalf("volume root %q must not be used as a git checkpoint root", volumeRoot)
	}

	dir := t.TempDir()
	if !gitCheckpointRepoAllowed(dir) {
		t.Fatalf("a project directory %q must remain eligible for git checkpoints", dir)
	}
}
