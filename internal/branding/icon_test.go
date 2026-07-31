package branding

import (
	"bytes"
	"image/png"
	"testing"
)

func TestEmbeddedIcons(t *testing.T) {
	tests := []struct {
		name          string
		data          []byte
		width, height int
	}{
		{name: "application", data: AppIconPNG(), width: 256, height: 256},
		{name: "taskbar", data: TaskbarIconPNG(), width: 64, height: 64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if len(test.data) == 0 {
				t.Fatal("embedded icon is empty")
			}
			config, err := png.DecodeConfig(bytes.NewReader(test.data))
			if err != nil {
				t.Fatalf("decode icon: %v", err)
			}
			if config.Width != test.width || config.Height != test.height {
				t.Fatalf("icon dimensions = %dx%d, want %dx%d", config.Width, config.Height, test.width, test.height)
			}
			icon, err := png.Decode(bytes.NewReader(test.data))
			if err != nil {
				t.Fatalf("decode icon pixels: %v", err)
			}
			_, _, _, alpha := icon.At(0, 0).RGBA()
			if alpha != 0 {
				t.Fatalf("icon corner alpha = %d, want fully transparent", alpha)
			}
		})
	}
}
