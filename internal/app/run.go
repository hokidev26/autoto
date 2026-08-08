package app

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"autoto/internal/agent"
	"autoto/internal/channels"
	"autoto/internal/config"
)

// Options configures CLI or desktop-hosted runtime startup.
type Options struct {
	// ConfigPath is the resolved or unresolved config path. When empty, Run
	// parses --config from Args (or os.Args).
	ConfigPath string
	// Args are command-line arguments without the program name. When nil, Run
	// uses os.Args[1:].
	Args []string
	// EphemeralHTTP binds the main HTTP listener to host:0 so desktop shells
	// avoid clashing with a long-lived CLI instance on the configured port.
	EphemeralHTTP bool
	// Logger overrides the process logger. When nil, a stdout text logger is used.
	Logger *slog.Logger
	// OpenBrowser, when true, prints a startup banner and opens the web UI in
	// the default browser once the server is ready. The CLI entry point sets
	// this; desktop shells (which own a native window) leave it false. It can be
	// suppressed at runtime with --no-browser or AUTOTO_NO_BROWSER=1.
	OpenBrowser bool
}

type channelApprovalAdapter struct {
	runner *agent.Runner
}

func (a channelApprovalAdapter) ApproveToolCall(ctx context.Context, agentID, toolUseID string, decision channels.ApprovalDecision) (bool, error) {
	return a.runner.ApproveToolCall(ctx, agentID, toolUseID, agent.ToolApprovalDecision{
		Decision: decision.Decision, Reason: decision.Reason, DecidedBy: decision.DecidedBy,
		PermissionGeneration: decision.PermissionGeneration, PolicyGeneration: decision.PolicyGeneration,
	})
}

// logLevelFromEnv reads AUTOTO_LOG_LEVEL. The level was previously pinned to
// Info with no way to change it, which made every slog.Debug in the codebase
// permanently invisible and left diagnosing upstream behaviour — a provider not
// returning the headers it is supposed to, for instance — with nothing to look
// at. An unset or unrecognized value keeps the Info default.
func logLevelFromEnv(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Run is the canonical process entry for cmd/autoto and the legacy shim.
// Desktop shells should prefer NewRuntime + Start + Close so they can own the
// window lifecycle without process signals.
func Run(options Options) int {
	enableConsoleUTF8()

	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevelFromEnv(os.Getenv("AUTOTO_LOG_LEVEL"))}))
		slog.SetDefault(logger)
		options.Logger = logger
	}

	args := options.Args
	if args == nil {
		args = os.Args[1:]
	}
	noBrowser := false
	if options.ConfigPath == "" {
		flags := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
		configPath := flags.String("config", "", "path to config.json")
		noBrowserFlag := flags.Bool("no-browser", false, "do not open the web UI in a browser on startup")
		if err := flags.Parse(args); err != nil {
			return 2
		}
		options.ConfigPath = *configPath
		noBrowser = *noBrowserFlag
	}
	if envTruthy(os.Getenv("AUTOTO_NO_BROWSER")) {
		noBrowser = true
	}

	rt, err := NewRuntime(options)
	if err != nil {
		logger.Error("create runtime", "error", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := rt.Start(ctx); err != nil {
		logger.Error("start runtime", "error", err)
		_ = rt.Close(context.Background())
		return 1
	}

	if options.OpenBrowser {
		announceStartup(ctx, rt, logger, !noBrowser)
	}

	select {
	case <-ctx.Done():
	case <-rt.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rt.Close(shutdownCtx); err != nil {
		logger.Error("shutdown", "error", err)
		return 1
	}
	return 0
}

// announceStartup prints a human-facing startup banner with the local URL and,
// when launchBrowser is true, opens it in the default browser once the server
// answers /api/health. The banner always prints (even with --no-browser) so the
// URL stays visible; the browser launch runs in the background so it never
// blocks the run loop.
func announceStartup(ctx context.Context, rt *Runtime, logger *slog.Logger, launchBrowser bool) {
	url := rt.URL()
	if url == "" {
		return
	}
	mode := "production"
	if strings.Contains(config.Version, "dev") {
		mode = "development"
	}
	version := config.Version
	if config.Commit != "" {
		version += " (" + config.Commit + ")"
	}
	fmt.Fprintf(os.Stdout, "\n  Autoto %s\n  ➜  %s\n  ➜  mode: %s\n\n", version, url, mode)

	if !launchBrowser {
		return
	}
	go func() {
		readyCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		if err := rt.WaitReady(readyCtx); err != nil {
			logger.Warn("web UI not ready before browser launch", "error", err)
		}
		if err := openBrowser(url); err != nil {
			logger.Warn("open browser", "error", err, "url", url)
		}
	}()
}
