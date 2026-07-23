//go:build windows

package app

import "golang.org/x/sys/windows"

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
)

// enableConsoleUTF8 switches the Windows console output code page to UTF-8 so
// the startup banner (→) and any non-ASCII log text render correctly instead
// of as mojibake in legacy code pages (e.g. Big5/CP950). Best effort: failures
// are ignored because the process still runs fine with the default code page.
func enableConsoleUTF8() {
	const cpUTF8 = 65001
	_, _, _ = procSetConsoleOutputCP.Call(uintptr(cpUTF8))
}
