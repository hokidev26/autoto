package app

import (
	"fmt"
	"net"
	"testing"
	"time"

	"autoto/internal/config"
)

// A desktop launch prefers a fixed port so the browser keeps the same origin, and
// with it the localStorage that holds read/unread marks, panel widths and drafts.
// The first refusal used to be final, so a restart -- where the previous instance
// is still closing its listener -- silently came back on an OS-assigned port with
// an empty store.
//
// The preferred port is redirected to one this test owns. Asserting against the
// real 16889 would only work on a machine where no Autoto is already running.
func reserveStablePort(t *testing.T) (string, net.Listener) {
	t.Helper()
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := holder.Addr().(*net.TCPAddr).Port
	previous := desktopStablePortOverride
	desktopStablePortOverride = port
	t.Cleanup(func() { desktopStablePortOverride = previous })
	return fmt.Sprintf("127.0.0.1:%d", port), holder
}

func ephemeralDesktopConfig() config.Config {
	cfg := config.Config{}
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 16888
	return cfg
}

func TestARestartWaitsForTheStablePortInsteadOfLosingItsOrigin(t *testing.T) {
	stable, departing := reserveStablePort(t)

	// Released shortly after the launch begins, the way a restarting process does.
	go func() {
		time.Sleep(300 * time.Millisecond)
		departing.Close()
	}()

	listener, _, err := bindConfiguredHTTPListeners(ephemeralDesktopConfig(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if got := listener.Addr().String(); got != stable {
		t.Fatalf("bound %s, want the stable %s so the origin survives the restart", got, stable)
	}
}

func TestAnOccupiedStablePortStillStartsOnAnEphemeralPort(t *testing.T) {
	// A second concurrent instance must not hang the launch. It loses persistence
	// across launches, which is the documented cost of running two at once.
	stable, holder := reserveStablePort(t)
	defer holder.Close()

	started := time.Now()
	listener, _, err := bindConfiguredHTTPListeners(ephemeralDesktopConfig(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	if listener.Addr().String() == stable {
		t.Fatal("the stable port is held, so the launch must fall back")
	}
	if waited := time.Since(started); waited > 5*time.Second {
		t.Fatalf("waited %s before falling back; the bound must keep a launch responsive", waited)
	}
}

func TestAFreeStablePortIsTakenWithoutWaiting(t *testing.T) {
	stable, holder := reserveStablePort(t)
	holder.Close()

	started := time.Now()
	listener, _, err := bindConfiguredHTTPListeners(ephemeralDesktopConfig(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	if got := listener.Addr().String(); got != stable {
		t.Fatalf("bound %s, want %s", got, stable)
	}
	// The retry must not add latency to the ordinary case where the port is free.
	if waited := time.Since(started); waited > time.Second {
		t.Fatalf("took %s to bind a free port", waited)
	}
}
