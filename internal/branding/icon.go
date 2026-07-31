// Package branding exposes the canonical Autoto visual assets shared by the
// browser CLI and the native desktop shell.
package branding

import _ "embed"

//go:embed assets/app-icon-256.png
var appIconPNG []byte

//go:embed assets/taskbar-icon-64.png
var taskbarIconPNG []byte

// AppIconPNG returns the canonical Autoto application icon as PNG bytes.
// Callers must treat the returned bytes as read-only.
func AppIconPNG() []byte {
	return appIconPNG
}

// TaskbarIconPNG returns the high-contrast Windows taskbar variant.
// It keeps the same face geometry on a white app tile so the mark survives
// scaling to 16 or 32 physical pixels.
func TaskbarIconPNG() []byte {
	return taskbarIconPNG
}
