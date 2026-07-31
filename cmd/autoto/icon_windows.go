//go:build windows

package main

import (
	"runtime"
	"unsafe"

	"autoto/internal/branding"
	"golang.org/x/sys/windows"
)

const (
	wmSetIcon  = 0x0080
	iconSmall  = 0
	iconBig    = 1
	smCXIcon   = 11
	smCYIcon   = 12
	smCXSmIcon = 49
	smCYSmIcon = 50
)

var (
	kernel32                   = windows.NewLazySystemDLL("kernel32.dll")
	user32                     = windows.NewLazySystemDLL("user32.dll")
	procGetConsoleWindow       = kernel32.NewProc("GetConsoleWindow")
	procCreateIconFromResource = user32.NewProc("CreateIconFromResourceEx")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procSendMessage            = user32.NewProc("SendMessageW")
)

// setPlatformIcon updates the classic Windows console host used by the browser
// entrypoint. The browser window remains owned by the user's default browser.
func setPlatformIcon() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	data := branding.TaskbarIconPNG()

	if icon := createWindowsIcon(data, systemMetric(smCXIcon, 32), systemMetric(smCYIcon, 32)); icon != 0 {
		procSendMessage.Call(hwnd, wmSetIcon, iconBig, icon)
	}
	if icon := createWindowsIcon(data, systemMetric(smCXSmIcon, 16), systemMetric(smCYSmIcon, 16)); icon != 0 {
		procSendMessage.Call(hwnd, wmSetIcon, iconSmall, icon)
	}
}

func systemMetric(index int, fallback int) int {
	value, _, _ := procGetSystemMetrics.Call(uintptr(index))
	if value == 0 {
		return fallback
	}
	return int(value)
}

func createWindowsIcon(data []byte, width int, height int) uintptr {
	if len(data) < 8 || width <= 0 || height <= 0 {
		return 0
	}
	icon, _, _ := procCreateIconFromResource.Call(
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		1,
		0x00030000,
		uintptr(width),
		uintptr(height),
		0,
	)
	runtime.KeepAlive(data)
	return icon
}
