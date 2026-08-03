package tools

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ResolveWorkdirWithin returns a canonical existing directory that remains
// inside parent. It resolves symlinks and junctions before checking the
// boundary, so lexical tricks cannot escape the parent workspace.
func ResolveWorkdirWithin(parent, requested string) (string, error) {
	parent = strings.TrimSpace(parent)
	requested = strings.TrimSpace(requested)
	if parent == "" {
		return "", errors.New("parent workspace is required")
	}
	if hasParentSegment(requested) {
		return "", errors.New("workdir cannot contain ..")
	}
	parentAbs, err := filepath.Abs(filepath.Clean(parent))
	if err != nil {
		return "", errors.New("parent workspace path is invalid")
	}
	parentReal, err := filepath.EvalSymlinks(parentAbs)
	if err != nil {
		return "", errors.New("parent workspace is unavailable")
	}
	parentInfo, err := os.Stat(parentReal)
	if err != nil || !parentInfo.IsDir() {
		return "", errors.New("parent workspace is not a directory")
	}

	target := parentAbs
	if requested != "" {
		if filepath.IsAbs(requested) {
			target = requested
		} else {
			target = filepath.Join(parentAbs, requested)
		}
	}
	targetAbs, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", errors.New("workdir path is invalid")
	}
	targetReal, err := filepath.EvalSymlinks(targetAbs)
	if err != nil {
		return "", errors.New("workdir is unavailable")
	}
	targetInfo, err := os.Stat(targetReal)
	if err != nil || !targetInfo.IsDir() {
		return "", errors.New("workdir is not a directory")
	}
	rel, err := filepath.Rel(parentReal, targetReal)
	if err != nil || filepath.IsAbs(rel) || isOutsideRelative(rel) {
		return "", errors.New("workdir must remain inside parent workspace")
	}
	return filepath.Clean(targetReal), nil
}

func hasParentSegment(value string) bool {
	for _, segment := range strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == ".." {
			return true
		}
	}
	return false
}

func isOutsideRelative(rel string) bool {
	if rel == ".." {
		return true
	}
	if runtime.GOOS == "windows" {
		rel = strings.ReplaceAll(rel, "\\", "/")
	}
	return strings.HasPrefix(rel, "../")
}
