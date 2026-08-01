//go:build desktop

package desktop

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

const (
	windowStateFileName = "desktop-window.json"
	windowStateVersion  = 1
)

type windowState struct {
	Version   int  `json:"version"`
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	X         int  `json:"x"`
	Y         int  `json:"y"`
	Maximised bool `json:"maximised"`
	// HasPosition distinguishes "never saved" from origin (0,0).
	HasPosition bool `json:"hasPosition"`
}

func windowStatePath(homeDir string) string {
	if homeDir == "" {
		return ""
	}
	return filepath.Join(homeDir, windowStateFileName)
}

func loadWindowState(path string) (windowState, bool) {
	if path == "" {
		return windowState{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return windowState{}, false
	}
	var state windowState
	if err := json.Unmarshal(data, &state); err != nil {
		return windowState{}, false
	}
	if state.Version != windowStateVersion {
		return windowState{}, false
	}
	if state.Width < 400 || state.Height < 300 {
		return windowState{}, false
	}
	return state, true
}

func saveWindowState(path string, state windowState) error {
	if path == "" {
		return nil
	}
	state.Version = windowStateVersion
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// applyWindowState sets initial geometry before Show when a saved state exists.
func applyWindowState(window *application.WebviewWindow, state windowState) {
	if window == nil {
		return
	}
	// The saved Width/Height are already applied through the window options in
	// the caller (NewWithOptions), so there is no need to SetSize here -- and
	// WebviewWindow.SetSize has no nil-impl guard, so calling it before the
	// window is shown panics in the Wails runtime this shell builds against.
	// SetPosition, Center and Maximise are all guarded: they no-op (or defer
	// to options) while the window impl is still nil.
	if state.HasPosition {
		window.SetPosition(state.X, state.Y)
	} else {
		window.Center()
	}
	if state.Maximised {
		window.Maximise()
	}
}

// attachWindowStatePersistence debounces geometry writes on move/resize.
func attachWindowStatePersistence(window *application.WebviewWindow, path string, logger *slog.Logger) {
	if window == nil || path == "" {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	// Seed from current geometry so a maximise-before-move still has a size.
	w0, h0 := window.Size()
	x0, y0 := window.Position()
	var (
		mu      sync.Mutex
		timer   *time.Timer
		pending = windowState{
			Width:       w0,
			Height:      h0,
			X:           x0,
			Y:           y0,
			Maximised:   window.IsMaximised(),
			HasPosition: true,
		}
	)
	flush := func() {
		mu.Lock()
		state := pending
		mu.Unlock()
		if state.Width < 400 || state.Height < 300 {
			return
		}
		if err := saveWindowState(path, state); err != nil {
			logger.Debug("save window state", "error", err, "path", path)
		}
	}
	schedule := func() {
		if window.IsMaximised() {
			mu.Lock()
			pending.Maximised = true
			// Keep last normal geometry for restore after un-maximise.
			mu.Unlock()
		} else {
			w, h := window.Size()
			x, y := window.Position()
			mu.Lock()
			pending = windowState{
				Width:       w,
				Height:      h,
				X:           x,
				Y:           y,
				Maximised:   false,
				HasPosition: true,
			}
			mu.Unlock()
		}
		mu.Lock()
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(350*time.Millisecond, flush)
		mu.Unlock()
	}

	window.OnWindowEvent(events.Common.WindowDidResize, func(*application.WindowEvent) { schedule() })
	window.OnWindowEvent(events.Common.WindowDidMove, func(*application.WindowEvent) { schedule() })
	window.OnWindowEvent(events.Common.WindowMaximise, func(*application.WindowEvent) { schedule() })
	window.OnWindowEvent(events.Common.WindowUnMaximise, func(*application.WindowEvent) { schedule() })
	window.OnWindowEvent(events.Common.WindowRestore, func(*application.WindowEvent) { schedule() })
}
