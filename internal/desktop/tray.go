//go:build desktop

package desktop

import (
	"fmt"
	"log/slog"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/icons"
)

// attachSystemTray adds a minimal tray menu: Show, Hide, Autostart, Quit.
// Closing the window hides to tray instead of quitting; Quit exits the shell.
func attachSystemTray(app *application.App, window *application.WebviewWindow, logger *slog.Logger, life *lifecycleHost) {
	if app == nil || window == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}

	tray := app.SystemTray.New()
	tray.SetTooltip("Autoto")
	if runtime.GOOS == "darwin" {
		if len(trayIconPNG) > 0 {
			tray.SetIcon(trayIconPNG)
		} else {
			tray.SetTemplateIcon(icons.SystrayMacTemplate)
		}
	} else if len(trayIconPNG) > 0 {
		tray.SetIcon(trayIconPNG)
	} else if len(trayIconSmallPNG) > 0 {
		tray.SetIcon(trayIconSmallPNG)
	} else {
		tray.SetIcon(icons.SystrayMacTemplate)
	}

	menu := app.NewMenu()
	menu.Add("Show Autoto").OnClick(func(ctx *application.Context) {
		window.Show()
		window.Restore()
		window.Focus()
	})
	menu.Add("Hide").OnClick(func(ctx *application.Context) {
		window.Hide()
	})
	menu.AddSeparator()
	menu.Add("Enable Login Item").OnClick(func(ctx *application.Context) {
		if life == nil {
			return
		}
		if err := life.AutostartEnable(); err != nil {
			logger.Warn("enable autostart", "error", err)
			app.Dialog.Error().SetTitle("Autoto").SetMessage(fmt.Sprintf("Could not enable login item:\n%v", err)).Show()
			return
		}
		logger.Info("autostart enabled")
	})
	menu.Add("Disable Login Item").OnClick(func(ctx *application.Context) {
		if life == nil {
			return
		}
		if err := life.AutostartDisable(); err != nil {
			logger.Warn("disable autostart", "error", err)
			app.Dialog.Error().SetTitle("Autoto").SetMessage(fmt.Sprintf("Could not disable login item:\n%v", err)).Show()
			return
		}
		logger.Info("autostart disabled")
	})
	menu.AddSeparator()
	menu.Add("Quit").OnClick(func(ctx *application.Context) {
		logger.Info("quit requested from system tray")
		app.Quit()
	})
	tray.SetMenu(menu)
	tray.OnClick(func() {
		if window.IsVisible() {
			window.Hide()
			return
		}
		window.Show()
		window.Restore()
		window.Focus()
	})

	// Closing the window quits. It used to hide to tray and cancel the close, which
	// reads as a stuck app: the window disappears, so the app looks closed, while the
	// server, its database handle and every managed child process keep running with
	// nothing on screen accounting for them. The desktop build has no console either,
	// so there is not even a stray terminal left as a hint that it is still alive.
	//
	// Hiding is still available, deliberately, from the tray menu and by clicking the
	// tray icon. The difference is that those are asking for it, and this is not.
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		logger.Info("main window closed; quitting desktop shell")
		app.Quit()
	})
}
