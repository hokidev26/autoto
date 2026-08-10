//go:build desktop

package desktop

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"autoto/internal/config"
)

func writeTestConfig(t *testing.T, home string, port int) string {
	t.Helper()
	configPath := filepath.Join(home, "config.json")
	cfg := config.Config{
		SchemaVersion: config.CurrentConfigVersion,
		Server:        config.ServerConfig{Host: "127.0.0.1", Port: port},
		Paths: config.PathsConfig{
			HomeDir:           home,
			DatabasePath:      filepath.Join(home, "autoto.db"),
			DefaultProjectDir: filepath.Join(home, "projects"),
		},
		Agent: config.AgentConfig{DefaultModel: "openai:gpt-4.1-mini", SummaryModel: "openai:gpt-4.1-mini"},
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestRunHeadlessStartsAndStopsCleanly(t *testing.T) {
	home := t.TempDir()
	// The configured port is the desktop's own preferred port on purpose. Any other
	// value makes bindConfiguredHTTPListeners try 16889 first, and on a machine where
	// Autoto is already running that retry loop outlasts this test's cancel window.
	// desktopStableAddr declines to claim the configured CLI port, so this skips it.
	configPath := writeTestConfig(t, home, 16889)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			ConfigPath:    configPath,
			EphemeralHTTP: true,
			ReadyTimeout:  10 * time.Second,
			Headless:      true,
		})
	}()

	// Allow Start + WaitReady, then cancel the shell.
	time.Sleep(800 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("headless Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("headless Run did not exit after context cancel")
	}
}

func TestRunFailsWhenContextAlreadyCancelled(t *testing.T) {
	home := t.TempDir()
	configPath := writeTestConfig(t, home, 16889)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Run(ctx, Options{
		ConfigPath:    configPath,
		EphemeralHTTP: true,
		ReadyTimeout:  2 * time.Second,
		Headless:      true,
	})
	if err == nil {
		t.Fatal("expected cancelled context to fail start/ready")
	}
}
