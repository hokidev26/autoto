//go:build !windows

package app

// enableConsoleUTF8 is a no-op on platforms whose consoles are already UTF-8.
func enableConsoleUTF8() {}
