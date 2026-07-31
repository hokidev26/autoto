//go:build desktop && !windows

package desktop

import (
	"log/slog"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func attachMainWindowIcon(_ *application.App, _ *application.WebviewWindow, _ *slog.Logger) {}
