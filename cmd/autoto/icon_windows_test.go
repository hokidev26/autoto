//go:build windows

package main

import (
	"testing"

	"autoto/internal/branding"
	"golang.org/x/sys/windows"
)

func TestCreateWindowsIconFromBrandingPNG(t *testing.T) {
	icon := createWindowsIcon(branding.TaskbarIconPNG(), 16, 16)
	if icon == 0 {
		t.Fatal("CreateIconFromResourceEx returned a null icon")
	}
	destroyIcon := windows.NewLazySystemDLL("user32.dll").NewProc("DestroyIcon")
	if result, _, _ := destroyIcon.Call(icon); result == 0 {
		t.Fatal("DestroyIcon failed")
	}
}
