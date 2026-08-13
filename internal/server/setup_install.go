package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"

	"autoto/internal/process"
)

const (
	setupInstallTimeout     = 15 * time.Minute
	setupInstallOutputLimit = 8 * 1024
)

const (
	setupInstallStatusRunning   = "running"
	setupInstallStatusSucceeded = "succeeded"
	setupInstallStatusFailed    = "failed"
)

// setupInstallTargets maps the tool ids the wizard may request onto the
// install-command targets known to setupInstallCommand. Anything outside this
// allowlist is rejected, so the endpoint can never run a client-chosen command.
var setupInstallTargets = map[string]string{
	"git":  "git",
	"go":   "go",
	"node": "node",
}

type setupInstallJob struct {
	Tool       string `json:"tool"`
	Status     string `json:"status"`
	Command    string `json:"command"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

type setupInstallRequest struct {
	Tool string `json:"tool"`
}

type setupInstallStartResponse struct {
	Job setupInstallJob `json:"job"`
}

type setupInstallStatusResponse struct {
	Jobs []setupInstallJob `json:"jobs"`
}

func (s *Server) setupInstallProbeForRequest() setupProbe {
	if s.setupProbeFactory != nil {
		probe := s.setupProbeFactory()
		return normalizeSetupProbe(probe)
	}
	return defaultSetupProbe()
}

func (s *Server) runSetupInstall(ctx context.Context, command string, args ...string) (string, error) {
	if s.setupInstallRunner != nil {
		return s.setupInstallRunner(ctx, command, args...)
	}
	return runBoundedSetupInstallCommand(ctx, command, args...)
}

func runBoundedSetupInstallCommand(ctx context.Context, executable string, args ...string) (string, error) {
	output := &boundedSetupOutput{limit: setupInstallOutputLimit}
	command := exec.CommandContext(ctx, executable, args...)
	// Package-manager installs run headless behind the wizard; they must not
	// flash a console window on Windows.
	process.HideWindow(command)
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	return string(output.data), err
}

// setupInstall starts a package-manager install for one allowlisted tool.
// POST /api/setup/install {"tool":"git"}
func (s *Server) setupInstall(w http.ResponseWriter, r *http.Request) {
	var req setupInstallRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	tool := strings.ToLower(strings.TrimSpace(req.Tool))
	target, allowed := setupInstallTargets[tool]
	if !allowed {
		writeError(w, http.StatusBadRequest, "unsupported tool")
		return
	}

	probe := s.setupInstallProbeForRequest()
	packageManager := detectPackageManager(probe)
	if !packageManager.Available {
		writeError(w, http.StatusConflict, "未偵測到可用的套件管理器，請改用複製指令手動安裝。")
		return
	}
	commandLine := setupInstallCommand(packageManager.Name, target)
	if commandLine == "" {
		writeError(w, http.StatusConflict, "此工具沒有對應的自動安裝指令。")
		return
	}
	argv := strings.Fields(commandLine)
	executable, err := probe.LookPath(argv[0])
	if err != nil {
		writeError(w, http.StatusConflict, "套件管理器不可用，請改用複製指令手動安裝。")
		return
	}

	s.setupInstallMu.Lock()
	if s.setupInstallJobs == nil {
		s.setupInstallJobs = map[string]*setupInstallJob{}
	}
	for _, job := range s.setupInstallJobs {
		if job.Status == setupInstallStatusRunning {
			running := *job
			s.setupInstallMu.Unlock()
			// One install at a time: package managers hold global locks and
			// concurrent runs fail in confusing ways.
			writeJSON(w, http.StatusConflict, setupInstallStartResponse{Job: running})
			return
		}
	}
	job := &setupInstallJob{
		Tool:      tool,
		Status:    setupInstallStatusRunning,
		Command:   commandLine,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	s.setupInstallJobs[tool] = job
	started := *job
	s.setupInstallMu.Unlock()

	go s.executeSetupInstall(tool, executable, argv[1:])

	writeJSON(w, http.StatusAccepted, setupInstallStartResponse{Job: started})
}

func (s *Server) executeSetupInstall(tool, executable string, args []string) {
	ctx, cancel := context.WithTimeout(context.Background(), setupInstallTimeout)
	defer cancel()
	output, err := s.runSetupInstall(ctx, executable, args...)

	s.setupInstallMu.Lock()
	job := s.setupInstallJobs[tool]
	if job != nil {
		job.Output = output
		job.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			job.Status = setupInstallStatusFailed
			job.Error = err.Error()
		} else {
			job.Status = setupInstallStatusSucceeded
		}
	}
	s.setupInstallMu.Unlock()

	if err == nil {
		// A fresh install usually lands outside the PATH this process started
		// with (installers update the registry, not running processes), so pull
		// the machine/user PATH back in before the next detection pass.
		refreshProcessPathAfterInstall()
		s.invalidateSetupStatusCache()
	}
}

func (s *Server) invalidateSetupStatusCache() {
	s.setupStatusMu.Lock()
	s.setupStatusCacheAt = time.Time{}
	s.setupStatusMu.Unlock()
}

// setupInstallStatus reports every install job started in this process.
// GET /api/setup/install/status
func (s *Server) setupInstallStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.setupInstallMu.Lock()
	jobs := make([]setupInstallJob, 0, len(s.setupInstallJobs))
	for _, job := range s.setupInstallJobs {
		jobs = append(jobs, *job)
	}
	s.setupInstallMu.Unlock()
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Tool < jobs[j].Tool })
	writeJSON(w, http.StatusOK, setupInstallStatusResponse{Jobs: jobs})
}
