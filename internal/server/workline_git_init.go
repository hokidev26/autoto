package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const gitInitializationTimeout = 2 * time.Minute

type gitRepositoryState struct {
	Root    string
	HasHead bool
}

func isNotGitRepositoryError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not a git repository") || strings.Contains(message, "not a git repo")
}

func isMissingGitHeadError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"ambiguous argument 'head'",
		"ambiguous argument \"head\"",
		"unknown revision",
		"bad revision",
		"needed a single revision",
		"does not have any commits yet",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func inspectGitRepository(ctx context.Context, path string) (gitRepositoryState, error) {
	repoRoot, _, err := runGitCommand(ctx, path, 4096, 3*time.Second, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		if isNotGitRepositoryError(err) {
			return gitRepositoryState{}, nil
		}
		return gitRepositoryState{}, err
	}
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return gitRepositoryState{}, nil
	}
	if _, _, err := runGitCommand(ctx, repoRoot, 256, 3*time.Second, nil, "rev-parse", "--verify", "HEAD"); err != nil {
		if isMissingGitHeadError(err) {
			return gitRepositoryState{Root: repoRoot}, nil
		}
		return gitRepositoryState{}, err
	}
	return gitRepositoryState{Root: repoRoot, HasHead: true}, nil
}

// discoverGitSubdirectories scans one visible directory level only. Hidden
// implementation/cache directories are intentionally ignored: silently choosing
// one of those can fork a completely unrelated repository.
func discoverGitSubdirectories(ctx context.Context, root string) ([]gitRepositoryState, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	result := make([]gitRepositoryState, 0, 1)
	seen := make(map[string]struct{})
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		candidate := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(candidate, ".git")); err != nil {
			continue
		}
		repository, err := inspectGitRepository(ctx, candidate)
		if err != nil {
			return nil, err
		}
		if repository.Root == "" {
			continue
		}
		key := strings.ToLower(filepath.Clean(repository.Root))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, repository)
	}
	return result, nil
}

func resolveGitRepository(ctx context.Context, sourcePath string) (gitRepositoryState, []gitRepositoryState, error) {
	repository, err := inspectGitRepository(ctx, sourcePath)
	if err != nil || repository.Root != "" {
		return repository, nil, err
	}
	candidates, err := discoverGitSubdirectories(ctx, sourcePath)
	if err != nil {
		return gitRepositoryState{}, nil, err
	}
	if len(candidates) == 1 {
		return candidates[0], candidates, nil
	}
	return gitRepositoryState{}, candidates, nil
}

func gitCandidatePaths(candidates []gitRepositoryState) []string {
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Root != "" {
			paths = append(paths, candidate.Root)
		}
	}
	return paths
}

func writeNoGitRepoError(w http.ResponseWriter, path string) {
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"error": "\"" + path + "\" is not a git repository",
		"code":  "no_git_repo",
		"path":  path,
	})
}

func writeNoGitCommitsError(w http.ResponseWriter, path string) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": "\"" + path + "\" is a git repository but has no commits yet",
		"code":  "git_no_commits",
		"path":  path,
	})
}

func writeMultipleGitReposError(w http.ResponseWriter, path string, candidates []gitRepositoryState) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":      "multiple git repositories were found under \"" + path + "\"; configure the project to point to the intended repository",
		"code":       "multiple_git_repos",
		"path":       path,
		"candidates": gitCandidatePaths(candidates),
	})
}

func writeGitInitializationSuccess(w http.ResponseWriter, repository gitRepositoryState, initialized bool) {
	writeJSON(w, http.StatusOK, map[string]any{
		"repoRoot":    repository.Root,
		"path":        repository.Root,
		"initialized": initialized,
	})
}

// initProjectGit is idempotent. It initializes the configured project path (or
// repairs its single visible unborn child repository), stages the initial tree,
// creates an initial commit, and verifies that HEAD now resolves.
func (s *Server) initProjectGit(w http.ResponseWriter, r *http.Request) {
	project, err := s.store.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeWorklineWorkflowError(w, r, err)
		return
	}
	targetPath := strings.TrimSpace(project.GitPath)
	if targetPath == "" {
		writeError(w, http.StatusBadRequest, "project git path is not configured")
		return
	}
	if err := validateDir(targetPath); err != nil {
		s.writeWorklineWorkflowError(w, r, err)
		return
	}

	repository, candidates, err := resolveGitRepository(r.Context(), targetPath)
	if err != nil {
		s.writeGitError(w, r, err)
		return
	}
	if len(candidates) > 1 {
		writeMultipleGitReposError(w, targetPath, candidates)
		return
	}
	if repository.Root != "" {
		if !s.projectAllowsRepoRoot(project, repository.Root) {
			s.writeGitError(w, r, gitCommandError{Status: http.StatusForbidden, Msg: "git repository is outside the configured project boundary"})
			return
		}
		targetPath = repository.Root
		if repository.HasHead {
			writeGitInitializationSuccess(w, repository, false)
			return
		}
	} else {
		if _, _, err := runGitCommand(r.Context(), targetPath, 4096, 15*time.Second, nil, "init"); err != nil {
			s.writeGitError(w, r, err)
			return
		}
		repository, err = inspectGitRepository(r.Context(), targetPath)
		if err != nil || repository.Root == "" {
			if err == nil {
				err = gitCommandError{Status: http.StatusInternalServerError, Msg: "git init completed but the repository root could not be resolved"}
			}
			s.writeGitError(w, r, err)
			return
		}
		targetPath = repository.Root
	}

	if _, _, err := runGitCommand(r.Context(), targetPath, 4096, gitInitializationTimeout, nil, "add", "."); err != nil {
		s.writeGitError(w, r, err)
		return
	}
	// -c is a git-global option and must precede the commit subcommand. Keeping
	// the identity command-local avoids mutating the user's git configuration.
	if _, _, err := runGitCommand(r.Context(), targetPath, 4096, gitInitializationTimeout, nil,
		"-c", "user.name=Autoto",
		"-c", "user.email=autoto@localhost",
		"commit", "--allow-empty", "-m", "initial",
	); err != nil {
		s.writeGitError(w, r, err)
		return
	}

	repository, err = inspectGitRepository(r.Context(), targetPath)
	if err != nil {
		s.writeGitError(w, r, err)
		return
	}
	if repository.Root == "" || !repository.HasHead {
		s.writeGitError(w, r, gitCommandError{Status: http.StatusInternalServerError, Msg: "git repository was initialized but HEAD is still unavailable"})
		return
	}
	writeGitInitializationSuccess(w, repository, true)
}
