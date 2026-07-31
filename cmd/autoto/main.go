package main

import (
	"os"

	"autoto/internal/app"
)

func main() {
	setPlatformIcon()
	os.Exit(app.Run(app.Options{OpenBrowser: true}))
}
