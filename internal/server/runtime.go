package server

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"autoto/internal/config"
	"autoto/internal/db"
)

type runtimeSummaryResponse struct {
	GeneratedAt string                 `json:"generatedAt"`
	Version     string                 `json:"version"`
	Server      runtimeServerSummary   `json:"server"`
	Process     runtimeProcessSummary  `json:"process"`
	Go          runtimeGoSummary       `json:"go"`
	Memory      runtimeMemorySummary   `json:"memory"`
	Paths       []runtimePathSummary   `json:"paths"`
	Agent       runtimeAgentSummary    `json:"agent"`
	Security    runtimeSecuritySummary `json:"security"`
	Providers   runtimeProviderStats   `json:"providers"`
	Backends    runtimeBackendStats    `json:"backends"`
}

type runtimeServerSummary struct {
	Address    string `json:"address"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	ConfigPath string `json:"configPath,omitempty"`
}

type runtimeProcessSummary struct {
	PID           int    `json:"pid"`
	Executable    string `json:"executable,omitempty"`
	StartedAt     string `json:"startedAt"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
}

type runtimeGoSummary struct {
	Version    string `json:"version"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	CPUs       int    `json:"cpus"`
	Goroutines int    `json:"goroutines"`
}

type runtimeMemorySummary struct {
	AllocBytes      uint64 `json:"allocBytes"`
	TotalAllocBytes uint64 `json:"totalAllocBytes"`
	SysBytes        uint64 `json:"sysBytes"`
	HeapAllocBytes  uint64 `json:"heapAllocBytes"`
	HeapInuseBytes  uint64 `json:"heapInuseBytes"`
	StackInuseBytes uint64 `json:"stackInuseBytes"`
	NextGCBytes     uint64 `json:"nextGcBytes"`
	GCCycles        uint32 `json:"gcCycles"`
}

type runtimePathSummary struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Path  string `json:"path"`
}

type runtimeAgentSummary struct {
	DefaultModel string `json:"defaultModel"`
	SummaryModel string `json:"summaryModel"`
	// SafetyModel is reported as the model actually in force, resolving the
	// fallback, so the value shown is the one that will judge risky actions.
	SafetyModel            string                      `json:"safetyModel"`
	ReviewModel            string                      `json:"reviewModel"`
	DefaultPermissionMode  string                      `json:"defaultPermissionMode"`
	DefaultStartInPlanMode bool                        `json:"defaultStartInPlanMode"`
	MaxTurns               int                         `json:"maxTurns"`
	FirstTokenTimeoutMs    int                         `json:"firstTokenTimeoutMs"`
	MaxTransientRetries    int                         `json:"maxTransientRetries"`
	Continuation           runtimeContinuationSettings `json:"continuation"`
}

type runtimeContinuationSettings struct {
	Mode             string `json:"mode"`
	SegmentTurns     int    `json:"segmentTurns"`
	MaxContinuations int    `json:"maxContinuations"`
	MaxTotalTurns    int    `json:"maxTotalTurns"`
	MaxRunDurationMs int64  `json:"maxRunDurationMs"`
	MaxRunTokens     int64  `json:"maxRunTokens"`
}

type runtimeProviderStats struct {
	Total      int `json:"total"`
	Configured int `json:"configured"`
}

type runtimeBackendStats struct {
	Configured int `json:"configured"`
	Active     int `json:"active"`
}

type runtimeSecuritySummary struct {
	Exposed                  bool               `json:"exposed"`
	CurrentRequestRemote     bool               `json:"currentRequestRemote"`
	RemoteAccessRequired     bool               `json:"remoteAccessRequired"`
	AccessPasswordConfigured bool               `json:"accessPasswordConfigured"`
	BypassPermissionsAllowed bool               `json:"bypassPermissionsAllowed"`
	RemoteTerminalAllowed    bool               `json:"remoteTerminalAllowed"`
	MaxPermissionMode        string             `json:"maxPermissionMode"`
	Mode                     string             `json:"mode"`
	Message                  string             `json:"message"`
	Capabilities             remoteCapabilities `json:"capabilities"`
}

func (s *Server) runtimeSummary(w http.ResponseWriter, r *http.Request) {
	cfg := s.configSnapshot()
	summary := buildRuntimeSummary(cfg, s.configPathSnapshot(), s.startedAt)
	summary.Providers.Configured = 0
	for _, provider := range cfg.Providers.Summaries() {
		if s.providerConfigured(provider) {
			summary.Providers.Configured++
		}
	}
	summary.Security = s.runtimeSecuritySummaryForRequest(r)
	writeJSON(w, http.StatusOK, summary)
}

func buildRuntimeSummary(cfg config.Config, configPath string, startedAt time.Time) runtimeSummaryResponse {
	now := time.Now().UTC()
	if startedAt.IsZero() {
		startedAt = now
	}
	startedAt = startedAt.UTC()
	if configPath == "" {
		configPath = effectiveConfigPath(cfg, "")
	}

	uptimeSeconds := int64(now.Sub(startedAt).Seconds())
	if uptimeSeconds < 0 {
		uptimeSeconds = 0
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	executable, _ := os.Executable()
	providers := cfg.Providers.Summaries()
	providerStats := runtimeProviderStats{Total: len(providers)}
	for _, provider := range providers {
		if provider.Configured {
			providerStats.Configured++
		}
	}
	backendStats := runtimeBackendStats{Configured: len(cfg.Backends.Instances)}
	for _, backend := range cfg.Backends.Instances {
		if backend.Active {
			backendStats.Active++
		}
	}

	securityHardening := cfg.Security.Exposed
	securityMessage := "本地模式：允许使用完整权限模式。"
	if securityHardening {
		securityMessage = "显式暴露模式已启用：需要访问密码，且禁用 bypassPermissions。"
	}

	return runtimeSummaryResponse{
		GeneratedAt: db.Now(),
		Version:     config.Version,
		Server: runtimeServerSummary{
			Address:    runtimeServerAddress(cfg),
			Host:       runtimeServerHost(cfg),
			Port:       runtimeServerPort(cfg),
			ConfigPath: configPath,
		},
		Process: runtimeProcessSummary{
			PID:           os.Getpid(),
			Executable:    executable,
			StartedAt:     startedAt.Format(time.RFC3339Nano),
			UptimeSeconds: uptimeSeconds,
		},
		Go: runtimeGoSummary{
			Version:    runtime.Version(),
			OS:         runtime.GOOS,
			Arch:       runtime.GOARCH,
			CPUs:       runtime.NumCPU(),
			Goroutines: runtime.NumGoroutine(),
		},
		Memory: runtimeMemorySummary{
			AllocBytes:      mem.Alloc,
			TotalAllocBytes: mem.TotalAlloc,
			SysBytes:        mem.Sys,
			HeapAllocBytes:  mem.HeapAlloc,
			HeapInuseBytes:  mem.HeapInuse,
			StackInuseBytes: mem.StackInuse,
			NextGCBytes:     mem.NextGC,
			GCCycles:        mem.NumGC,
		},
		Paths: []runtimePathSummary{
			{Key: "home", Label: "Autoto home", Path: cfg.Paths.HomeDir},
			{Key: "database", Label: "SQLite database", Path: cfg.Paths.DatabasePath},
			{Key: "config", Label: "Config file", Path: configPath},
			{Key: "projects", Label: "Default project directory", Path: cfg.Paths.DefaultProjectDir},
		},
		Agent: runtimeAgentSummary{
			DefaultModel:           cfg.Agent.DefaultModel,
			SummaryModel:           cfg.Agent.SummaryModel,
			SafetyModel:            firstNonEmptyModel(cfg.Agent.SafetyModel, cfg.Agent.SummaryModel),
			ReviewModel:            cfg.Agent.ReviewModel,
			DefaultPermissionMode:  cfg.Agent.DefaultPermissionMode,
			DefaultStartInPlanMode: cfg.Agent.DefaultStartInPlanMode,
			MaxTurns:               cfg.Agent.MaxTurns,
			FirstTokenTimeoutMs:    cfg.Agent.FirstTokenTimeoutMs,
			MaxTransientRetries:    cfg.Agent.MaxTransientRetries,
			Continuation:           runtimeContinuationSettings{Mode: cfg.Agent.AutoContinuationMode, SegmentTurns: cfg.Agent.ContinuationSegmentTurns, MaxContinuations: cfg.Agent.MaxContinuations, MaxTotalTurns: cfg.Agent.MaxTotalTurns, MaxRunDurationMs: cfg.Agent.MaxRunDurationMs, MaxRunTokens: cfg.Agent.MaxRunTokens},
		},
		Security: runtimeSecuritySummary{
			Exposed:                  cfg.Security.Exposed,
			RemoteAccessRequired:     securityHardening,
			AccessPasswordConfigured: strings.TrimSpace(cfg.Security.AccessPassword) != "",
			BypassPermissionsAllowed: !securityHardening,
			RemoteTerminalAllowed:    !securityHardening || cfg.Security.AllowRemoteTerminal,
			MaxPermissionMode:        mapBoolString(securityHardening, "acceptEdits", "bypassPermissions"),
			Mode:                     mapBoolString(securityHardening, "remote-hardened", "local"),
			Message:                  securityMessage,
		},
		Providers: providerStats,
		Backends:  backendStats,
	}
}

func runtimeServerAddress(cfg config.Config) string {
	return fmt.Sprintf("%s:%d", runtimeServerHost(cfg), runtimeServerPort(cfg))
}

func runtimeServerHost(cfg config.Config) string {
	host := strings.TrimSpace(cfg.Server.Host)
	if host == "" {
		return "localhost"
	}
	return host
}

func runtimeServerPort(cfg config.Config) int {
	if cfg.Server.Port == 0 {
		return 16888
	}
	return cfg.Server.Port
}

func mapBoolString(condition bool, whenTrue, whenFalse string) string {
	if condition {
		return whenTrue
	}
	return whenFalse
}

func (s *Server) runtimeSecuritySummaryForRequest(r *http.Request) runtimeSecuritySummary {
	cfg := s.configSnapshot()
	auth := s.remoteAccessAuthentication(r)
	capabilities := s.capabilitiesForRequest(r)
	configured, _ := s.credentialConfigured()
	hardening := auth.Remote && auth.Authenticated && auth.Mode == remoteAccessModeRestricted
	summary := runtimeSecuritySummary{
		Exposed:                  cfg.Security.Exposed,
		CurrentRequestRemote:     auth.Remote,
		RemoteAccessRequired:     auth.Remote,
		AccessPasswordConfigured: configured,
		BypassPermissionsAllowed: capabilities.MaxPermissionMode == "bypassPermissions",
		RemoteTerminalAllowed:    capabilities.TerminalAllowed,
		MaxPermissionMode:        capabilities.MaxPermissionMode,
		Mode:                     "local",
		Message:                  "本地模式：允许使用完整权限模式。",
		Capabilities:             capabilities,
	}
	if auth.Remote {
		summary.Mode = "remote-full"
		summary.Message = "完整远程会话已启用；安全设置仍只能在运行 Autoto 的主机通过 localhost 修改。"
	}
	if hardening {
		summary.Mode = "remote-restricted"
		summary.Message = "受限远程会话已启用：Run 被限制为 acceptEdits，终端和宿主机文件系统不可用。"
	}
	if auth.Remote && !auth.Authenticated {
		summary.Mode = "remote-unauthenticated"
		summary.Message = "远程访问需要认证。请配置 AUTOTO_ACCESS_PASSWORD 或在本地安全设置中创建访问密码。"
	}
	return summary
}

// firstNonEmptyModel mirrors Runner.SafetyModel's fallback so the reported value
// is what will actually run, not the raw configuration field.
func firstNonEmptyModel(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
