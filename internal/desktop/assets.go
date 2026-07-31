//go:build desktop

package desktop

import (
	_ "embed"

	"autoto/internal/branding"
)

var appIconPNG = branding.AppIconPNG()

//go:embed assets/tray-icon-32.png
var trayIconPNG []byte

//go:embed assets/tray-icon-16.png
var trayIconSmallPNG []byte
