//go:build desktop && !windows

package desktop

import "log/slog"

// The stable-profile problem is Windows-specific: only WebView2 derives its user
// data directory from the executable filename, so only there does replacing
// autoto-desktop-<build>.exe hand the window an empty localStorage. macOS and
// Linux key their WebView storage off the application identity, which is already
// stable across builds.
//
// Returning "" leaves Wails' own defaults in place, and WebviewUserDataPath is
// Windows-only in the options struct anyway.
func stableWebviewUserDataPath(*slog.Logger) string { return "" }
