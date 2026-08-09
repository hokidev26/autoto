package app

import (
	"fmt"
	"net"
	"strconv"
	"testing"

	"autoto/internal/config"
)

// The desktop shell used an OS-assigned port, so every launch produced a new
// origin. Browsers scope localStorage to an origin, so read/unread marks, panel
// widths, drafts, and language choices were silently discarded on every restart.
// A stable loopback port keeps the origin identical between launches.
func TestDesktopBindsAStablePortSoLocalStorageSurvivesRestarts(t *testing.T) {
	cfg := config.Config{}
	cfg.Server.Host = "localhost"
	cfg.Server.Port = 16888

	// Redirected to a port this test owns. Asserting against the real preferred
	// port made the test fail on any machine already running Autoto -- including the
	// developer's own -- which says nothing about the behaviour under test.
	stableAddr, probe := reserveStablePort(t)
	probe.Close()

	first, _, err := bindConfiguredHTTPListeners(cfg, true)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer first.Close()

	firstAddr := first.Addr().String()
	if firstAddr != stableAddr {
		t.Fatalf("desktop must prefer the stable port, got %s want %s", firstAddr, stableAddr)
	}
	// It must not take the port the CLI/browser instance uses.
	if desktopPreferredPort == cfg.Server.Port {
		t.Fatal("the desktop port must differ from the configured CLI port")
	}

	// A second concurrent instance cannot have the same port, so it falls back to
	// an OS-assigned one rather than failing to start.
	second, _, err := bindConfiguredHTTPListeners(cfg, true)
	if err != nil {
		t.Fatalf("second bind must fall back, not fail: %v", err)
	}
	defer second.Close()
	if second.Addr().String() == firstAddr {
		t.Fatal("two instances cannot share one port")
	}
	if _, port, splitErr := net.SplitHostPort(second.Addr().String()); splitErr != nil || port == "0" {
		t.Fatalf("fallback must be a real bound port, got %s", second.Addr().String())
	}
}

// When the user has configured the CLI onto the desktop's preferred port, the
// desktop must yield rather than steal it.
func TestDesktopYieldsWhenConfiguredPortMatches(t *testing.T) {
	stableAddr, probe := reserveStablePort(t)
	probe.Close()
	_, portText, err := net.SplitHostPort(stableAddr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{}
	cfg.Server.Host = "localhost"
	// The CLI is configured onto the very port the desktop would prefer.
	cfg.Server.Port = port

	if addr := desktopStableAddr(cfg); addr != "" {
		t.Fatalf("desktop must not claim the configured CLI port, got %q", addr)
	}

	listener, _, err := bindConfiguredHTTPListeners(cfg, true)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer listener.Close()
	if listener.Addr().String() == stableAddr {
		t.Fatal("desktop took the configured CLI port")
	}
}

// A non-ephemeral (CLI) start must keep honouring the configured address.
func TestNonEphemeralStartStillUsesConfiguredAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	cfg := config.Config{}
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = port

	bound, _, err := bindConfiguredHTTPListeners(cfg, false)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer bound.Close()
	if want := fmt.Sprintf("127.0.0.1:%d", port); bound.Addr().String() != want {
		t.Fatalf("CLI start must use the configured address, got %s want %s", bound.Addr().String(), want)
	}
}
