//go:build desktop && windows

package desktop

import (
	"log/slog"
	"sync"

	"autoto/internal/branding"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/w32"
)

// attachMainWindowIcon overrides both Windows window icon sizes after the native
// HWND exists. Wails currently installs only ICON_BIG, while the compact
// taskbar commonly asks for ICON_SMALL and otherwise falls back to a generic
// executable/window icon.
func attachMainWindowIcon(app *application.App, window *application.WebviewWindow, logger *slog.Logger) {
	iconData := branding.TaskbarIconPNG()
	if app == nil || window == nil || len(iconData) == 0 {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}

	var once sync.Once
	apply := func() {
		once.Do(func() {
			nativeWindow := window.NativeWindow()
			if nativeWindow == nil {
				logger.Warn("apply desktop window icon", "error", "native window is unavailable")
				return
			}
			hwnd := w32.HWND(uintptr(nativeWindow))

			largeIcon, largeErr := w32.CreateLargeHIconFromImage(iconData)
			if largeErr != nil {
				logger.Warn("create large desktop window icon", "error", largeErr)
			} else if largeIcon != 0 {
				w32.SendMessage(hwnd, w32.WM_SETICON, w32.ICON_BIG, uintptr(largeIcon))
			}

			smallIcon, smallErr := w32.CreateSmallHIconFromImage(iconData)
			if smallErr != nil {
				logger.Warn("create small desktop window icon", "error", smallErr)
			} else if smallIcon != 0 {
				w32.SendMessage(hwnd, w32.WM_SETICON, w32.ICON_SMALL, uintptr(smallIcon))
			}
		})
	}

	window.OnWindowEvent(events.Windows.WindowShow, func(_ *application.WindowEvent) {
		apply()
	})
	if window.NativeWindow() != nil {
		apply()
	}
}
