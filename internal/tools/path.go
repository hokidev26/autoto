package tools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"autoto/internal/workspacefs"
)

// resolveInCWD resolves the nearest existing ancestor before checking containment.
// This prevents paths such as workspace/link/outside.txt from escaping through an
// in-workspace symlink, including when the final path does not exist yet.
func resolveInCWD(cwd, inputPath string) (string, error) {
	if inputPath == "" {
		return "", errors.New("path is required")
	}
	if cwd == "" {
		cwd = "."
	}
	base, err := filepath.Abs(cwd)
	if err != nil {
		return "", errors.New("cannot resolve working directory")
	}
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	path := inputPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("cannot resolve path")
	}
	if !pathIsWithin(base, abs) {
		return "", errors.New("path escapes working directory")
	}
	if sensitiveToolPath(base, abs) {
		return "", errors.New("sensitive path is not accessible")
	}
	resolved, err := resolvePhysicalPath(abs)
	if err != nil {
		return "", errors.New("cannot resolve path")
	}
	if !pathWithin(realBase, resolved) {
		return "", errors.New("path escapes working directory")
	}
	if sensitiveToolPath(realBase, resolved) {
		return "", errors.New("sensitive path is not accessible")
	}
	return abs, nil
}

func resolvePhysicalPath(path string) (string, error) {
	path = filepath.Clean(path)
	missing := make([]string, 0)
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		missing = append(missing, filepath.Base(path))
		path = parent
	}
}

func pathWithin(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func pathIsWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func sensitiveToolPath(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil || filepath.IsAbs(rel) {
		return true
	}
	rel = filepath.ToSlash(rel)
	if workspacefs.IsSensitivePath(rel) {
		return true
	}
	for _, component := range strings.Split(strings.ToLower(rel), "/") {
		if component == ".git" {
			return true
		}
	}
	return false
}

func heavyToolDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".hg", ".svn", "node_modules", "vendor", "dist", "build", "target", ".next", ".nuxt", "coverage", "out",
		"__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", "venv", ".venv":
		return true
	default:
		return false
	}
}

func truncate(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	return s[:max] + "\n...[truncated]", true
}
