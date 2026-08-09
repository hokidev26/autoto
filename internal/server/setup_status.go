package server

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"autoto/internal/process"
)

const (
	setupVersionProbeTimeout  = 500 * time.Millisecond
	setupDatabaseProbeTimeout = 250 * time.Millisecond
	setupStatusCacheTTL       = 5 * time.Second
	setupVersionOutputLimit   = 4096
)

var setupVersionPattern = regexp.MustCompile(`v?\d+(?:\.\d+)+(?:[-+._]?[0-9A-Za-z]+)*`)

type setupStatusResponse struct {
	GeneratedAt    string                    `json:"generatedAt"`
	Platform       setupPlatformStatus       `json:"platform"`
	PackageManager setupPackageManagerStatus `json:"packageManager"`
	Database       setupDatabaseStatus       `json:"database"`
	Tools          []setupToolStatus         `json:"tools"`
}

type setupPlatformStatus struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type setupPackageManagerStatus struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

type setupDatabaseStatus struct {
	Available bool   `json:"available"`
	Required  bool   `json:"required"`
	Status    string `json:"status"`
}

type setupToolStatus struct {
	ID             string `json:"id"`
	Available      bool   `json:"available"`
	Required       bool   `json:"required"`
	Recommended    bool   `json:"recommended"`
	BuiltIn        bool   `json:"builtIn"`
	Version        string `json:"version"`
	InstallCommand string `json:"installCommand"`
}

type setupProbe struct {
	GOOS          string
	GOARCH        string
	Now           func() time.Time
	LookPath      func(string) (string, error)
	RunVersion    func(context.Context, string, ...string) (string, error)
	DatabaseReady func(context.Context) bool
}

type boundedSetupOutput struct {
	limit int
	data  []byte
}

func (w *boundedSetupOutput) Write(p []byte) (int, error) {
	if remaining := w.limit - len(w.data); remaining > 0 {
		if len(p) < remaining {
			remaining = len(p)
		}
		w.data = append(w.data, p[:remaining]...)
	}
	return len(p), nil
}

func defaultSetupProbe() setupProbe {
	return setupProbe{
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		Now:        time.Now,
		LookPath:   exec.LookPath,
		RunVersion: runBoundedSetupVersion,
	}
}

func runBoundedSetupVersion(ctx context.Context, executable string, args ...string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, setupVersionProbeTimeout)
	defer cancel()

	output := &boundedSetupOutput{limit: setupVersionOutputLimit}
	command := exec.CommandContext(probeCtx, executable, args...)
	// A version probe has no user at a console, so it must not be given one.
	process.HideWindow(command)
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return "", err
	}
	return string(output.data), nil
}

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	forceRefresh := r.URL.Query().Get("refresh") == "1"
	now := time.Now()

	s.setupStatusMu.Lock()
	if !forceRefresh && !s.setupStatusCacheAt.IsZero() && now.Sub(s.setupStatusCacheAt) < setupStatusCacheTTL {
		cached := s.setupStatusCache
		s.setupStatusMu.Unlock()
		writeJSON(w, http.StatusOK, cached)
		return
	}
	probe := defaultSetupProbe()
	probe.DatabaseReady = s.setupDatabaseReady
	status := detectSetupStatus(r.Context(), probe)
	s.setupStatusCache = status
	s.setupStatusCacheAt = now
	s.setupStatusMu.Unlock()
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) setupDatabaseReady(ctx context.Context) bool {
	if s == nil || s.store == nil || s.store.DB() == nil {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, setupDatabaseProbeTimeout)
	defer cancel()
	return s.store.DB().PingContext(probeCtx) == nil
}

func setupStatusHandler(probe setupProbe) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, detectSetupStatus(r.Context(), probe))
	}
}

func detectSetupStatus(ctx context.Context, probe setupProbe) setupStatusResponse {
	probe = normalizeSetupProbe(probe)
	packageManager := detectPackageManager(probe)
	shellCommand := "sh"
	shellVerificationArgs := []string{"-c", ":"}
	if probe.GOOS == "windows" {
		shellCommand = "cmd"
		shellVerificationArgs = []string{"/C", "exit", "0"}
	}

	tools := []setupToolStatus{
		detectSetupTool(ctx, probe, packageManager, setupToolDefinition{
			id: "shell", command: shellCommand, required: true, recommended: true, verificationArgs: shellVerificationArgs,
		}),
		{ID: "search", Available: true, Recommended: true, BuiltIn: true},
		detectSetupTool(ctx, probe, packageManager, setupToolDefinition{
			id: "git", command: "git", recommended: true, versionArgs: []string{"--version"}, installTarget: "git",
		}),
		detectSetupTool(ctx, probe, packageManager, setupToolDefinition{
			id: "go", command: "go", versionArgs: []string{"version"}, installTarget: "go",
		}),
		detectSetupTool(ctx, probe, packageManager, setupToolDefinition{
			id: "node", command: "node", versionArgs: []string{"--version"}, installTarget: "node",
		}),
	}

	databaseAvailable := probe.DatabaseReady(ctx)
	databaseStatus := "unavailable"
	if databaseAvailable {
		databaseStatus = "available"
	}
	return setupStatusResponse{
		GeneratedAt:    probe.Now().UTC().Format(time.RFC3339),
		Platform:       setupPlatformStatus{OS: probe.GOOS, Arch: probe.GOARCH},
		PackageManager: packageManager,
		Database:       setupDatabaseStatus{Available: databaseAvailable, Required: true, Status: databaseStatus},
		Tools:          tools,
	}
}

type setupToolDefinition struct {
	id               string
	command          string
	required         bool
	recommended      bool
	verificationArgs []string
	versionArgs      []string
	installTarget    string
}

func detectSetupTool(ctx context.Context, probe setupProbe, packageManager setupPackageManagerStatus, definition setupToolDefinition) setupToolStatus {
	status := setupToolStatus{
		ID: definition.id, Required: definition.required, Recommended: definition.recommended,
	}
	resolved, err := probe.LookPath(definition.command)
	if err == nil {
		if len(definition.verificationArgs) > 0 {
			if _, verificationErr := probe.RunVersion(ctx, resolved, definition.verificationArgs...); verificationErr != nil {
				return status
			}
		}
		status.Available = true
		if len(definition.versionArgs) > 0 {
			if output, versionErr := probe.RunVersion(ctx, resolved, definition.versionArgs...); versionErr == nil {
				status.Version = normalizeSetupVersion(output)
			}
		}
		return status
	}
	if definition.installTarget != "" && packageManager.Available {
		status.InstallCommand = setupInstallCommand(packageManager.Name, definition.installTarget)
	}
	return status
}

func normalizeSetupProbe(probe setupProbe) setupProbe {
	probe.GOOS = strings.ToLower(strings.TrimSpace(probe.GOOS))
	probe.GOARCH = strings.ToLower(strings.TrimSpace(probe.GOARCH))
	if probe.GOOS == "" {
		probe.GOOS = runtime.GOOS
	}
	if probe.GOARCH == "" {
		probe.GOARCH = runtime.GOARCH
	}
	if probe.Now == nil {
		probe.Now = time.Now
	}
	if probe.LookPath == nil {
		probe.LookPath = func(string) (string, error) { return "", errors.New("executable lookup unavailable") }
	}
	if probe.RunVersion == nil {
		probe.RunVersion = func(context.Context, string, ...string) (string, error) {
			return "", errors.New("version probe unavailable")
		}
	}
	if probe.DatabaseReady == nil {
		probe.DatabaseReady = func(context.Context) bool { return false }
	}
	return probe
}

func detectPackageManager(probe setupProbe) setupPackageManagerStatus {
	for _, candidate := range setupPackageManagerCandidates(probe.GOOS) {
		if _, err := probe.LookPath(candidate); err == nil {
			return setupPackageManagerStatus{Name: candidate, Available: true}
		}
	}
	return setupPackageManagerStatus{}
}

func setupPackageManagerCandidates(goos string) []string {
	switch goos {
	case "windows":
		return []string{"winget", "scoop", "choco"}
	case "darwin":
		return []string{"brew"}
	case "linux":
		return []string{"apt-get", "dnf", "pacman", "zypper"}
	default:
		return nil
	}
}

func normalizeSetupVersion(output string) string {
	version := setupVersionPattern.FindString(output)
	if len(version) > 64 {
		return version[:64]
	}
	return version
}

func setupInstallCommand(packageManager, target string) string {
	commands := map[string]map[string]string{
		"winget": {
			"git":  "winget install --id Git.Git --exact --accept-source-agreements --accept-package-agreements",
			"go":   "winget install --id GoLang.Go --exact --accept-source-agreements --accept-package-agreements",
			"node": "winget install --id OpenJS.NodeJS.LTS --exact --accept-source-agreements --accept-package-agreements",
		},
		"scoop": {
			"git": "scoop install git", "go": "scoop install go", "node": "scoop install nodejs-lts",
		},
		"choco": {
			"git": "choco install git -y", "go": "choco install golang -y", "node": "choco install nodejs-lts -y",
		},
		"brew": {
			"git": "brew install git", "go": "brew install go", "node": "brew install node",
		},
		"apt-get": {
			"git": "sudo apt-get install -y git", "go": "sudo apt-get install -y golang-go", "node": "sudo apt-get install -y nodejs npm",
		},
		"dnf": {
			"git": "sudo dnf install -y git", "go": "sudo dnf install -y golang", "node": "sudo dnf install -y nodejs npm",
		},
		"pacman": {
			"git": "sudo pacman -S --needed git", "go": "sudo pacman -S --needed go", "node": "sudo pacman -S --needed nodejs npm",
		},
		"zypper": {
			"git": "sudo zypper install -y git", "go": "sudo zypper install -y go", "node": "sudo zypper install -y nodejs npm",
		},
	}
	return commands[packageManager][target]
}
