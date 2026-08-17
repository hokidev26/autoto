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

// resolveGitRepository returns the single git repository that should own
// worklines for the project. The root itself takes priority; only when it has
// no usable repository do we fall back to a single nested subdirectory.
//
// A repository rooted at the user's HOME (or an ancestor of it) is treated
// as an accidental parent repository. Falling through lets discovery find the
// real repository one level down instead of routing a project through HOME.
//
// Discovery must also run when the chosen path is not a repository at all.
// Git only walks upward, so a parent directory never inherits a child's
// repository. On Windows, t.TempDir() usually lives under the user profile,
// so a stray ~/.git made inspectGitRepository succeed and accidentally
// reached discovery; macOS temp dirs live under /var/folders, which is not
// inside HOME, so the same tests never discovered the child until this
// empty-root path existed.
func resolveGitRepository(ctx context.Context, sourcePath string) (gitRepositoryState, []gitRepositoryState, error) {
	repository, err := inspectGitRepository(ctx, sourcePath)
	if err != nil {
		return repository, nil, err
	}
	if repository.Root != "" && !isUserHomeOrAncestor(repository.Root) {
		return repository, nil, nil
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

// isUserHomeOrAncestor reports whether the given path equals the user's HOME
// directory or is one of its ancestors. Git treats every directory between a
// repository's root and the working tree as part of the working tree, so a
// stray `~/.git` makes every nested directory look like it lives inside that
// repository. If the toplevel we got back is HOME (or above HOME, which only
// happens on path-resolution oddities), the candidate is clearly not the
// project the user meant to point at.
func isUserHomeOrAncestor(repoRoot string) bool {
	repoRoot = strings.TrimSpace(repoRoot)
	if repoRoot == "" {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return false
	}
	if pathWithin(repoRoot, home) {
		return true
	}
	return false
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

func (s *Server) writeNoGitRepoError(w http.ResponseWriter, r *http.Request, path string) {
	payload := map[string]any{"code": "no_git_repo"}
	if s.remoteAccessAuthentication(r).Remote {
		payload["error"] = genericRequestErrorMessage(http.StatusBadRequest)
		writeJSON(w, http.StatusBadRequest, payload)
		return
	}
	payload["error"] = "\"" + path + "\" is not a git repository"
	payload["path"] = path
	writeJSON(w, http.StatusBadRequest, payload)
}

func (s *Server) writeNoGitCommitsError(w http.ResponseWriter, r *http.Request, path string) {
	payload := map[string]any{"code": "git_no_commits"}
	if s.remoteAccessAuthentication(r).Remote {
		payload["error"] = genericRequestErrorMessage(http.StatusConflict)
		writeJSON(w, http.StatusConflict, payload)
		return
	}
	payload["error"] = "\"" + path + "\" is a git repository but has no commits yet"
	payload["path"] = path
	writeJSON(w, http.StatusConflict, payload)
}

func (s *Server) writeMultipleGitReposError(w http.ResponseWriter, r *http.Request, path string, candidates []gitRepositoryState) {
	payload := map[string]any{"code": "multiple_git_repos"}
	if s.remoteAccessAuthentication(r).Remote {
		payload["error"] = genericRequestErrorMessage(http.StatusConflict)
		writeJSON(w, http.StatusConflict, payload)
		return
	}
	payload["error"] = "multiple git repositories were found under \"" + path + "\"; configure the project to point to the intended repository"
	payload["path"] = path
	payload["candidates"] = gitCandidatePaths(candidates)
	writeJSON(w, http.StatusConflict, payload)
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
		s.writeMultipleGitReposError(w, r, targetPath, candidates)
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
