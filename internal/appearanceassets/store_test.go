package appearanceassets

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStoreImportCurrentOpenDeleteAndPermissions(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := pngData(t)
	metadata, err := store.Import(bytes.NewReader(data), "hero.png")
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.Revision) != 64 || metadata.ContentType != "image/png" || metadata.Width != 2 || metadata.Height != 2 || metadata.URL != "/appearance/backgrounds/"+metadata.Revision+"/hero.png" {
		t.Fatalf("metadata = %#v", metadata)
	}
	current, err := store.Current()
	if err != nil || current != metadata {
		t.Fatalf("Current() = %#v, %v", current, err)
	}
	resource, err := store.OpenResource(metadata.Revision, metadata.Filename)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := io.ReadAll(resource)
	resource.Close()
	if err != nil || !bytes.Equal(opened, data) {
		t.Fatalf("opened data mismatch: %v", err)
	}
	if runtime.GOOS != "windows" {
		for _, item := range []struct {
			path string
			mode os.FileMode
		}{
			{store.Root(), 0o700}, {filepath.Join(store.Root(), CurrentFilename), 0o600},
			{filepath.Join(store.Root(), metadata.Revision+".png"), 0o600},
		} {
			info, err := os.Stat(item.path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != item.mode {
				t.Errorf("%s mode %o, want %o", item.path, info.Mode().Perm(), item.mode)
			}
		}
	}
	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Current(); err != ErrNotFound {
		t.Fatalf("Current after delete = %v", err)
	}
	if _, err := store.OpenResource(metadata.Revision, metadata.Filename); err != ErrNotFound {
		t.Fatalf("OpenResource after delete = %v", err)
	}
}

func TestStoreContentAddressingAndSafety(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := pngData(t)
	first, err := store.Import(bytes.NewReader(data), "one.png")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Import(bytes.NewReader(data), "two.png")
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != second.Revision {
		t.Fatalf("same bytes got different revisions: %s != %s", first.Revision, second.Revision)
	}
	if _, err := store.OpenResource(first.Revision, first.Filename); err != ErrNotFound {
		t.Fatalf("old filename remained readable: %v", err)
	}
	if _, err := store.OpenResource(strings.Repeat("0", 64), "two.png"); err != ErrNotFound {
		t.Fatalf("invalid revision read = %v", err)
	}
	if _, err := store.Import(bytes.NewReader(data), "../escape.png"); err == nil {
		t.Fatal("accepted traversal filename")
	}
	if _, err := store.Import(bytes.NewReader(data), "x.svg"); err == nil {
		t.Fatal("accepted SVG filename")
	}
	pointer, err := os.ReadFile(filepath.Join(store.Root(), CurrentFilename))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(pointer, &decoded); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRejectsMalformedImage(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Import(bytes.NewReader([]byte("not an image")), "bad.png"); err == nil {
		t.Fatal("accepted malformed image")
	}
}

func pngData(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.Set(x, y, color.RGBA{R: 0x33, G: 0x66, B: 0xaa, A: 0xff})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
