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
	if len(metadata.Revision) != 64 || metadata.ContentType != "image/png" || metadata.Width != 2 || metadata.Height != 2 || metadata.URL != "/appearance/backgrounds/"+metadata.Revision+"/"+metadata.Revision+".png" {
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
		if runtime.GOOS != "windows" && info.Mode().Perm() != item.mode {
			t.Errorf("%s mode %o, want %o", item.path, info.Mode().Perm(), item.mode)
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

func TestStoreAcceptsUnicodeFilenamesWithoutUsingThemAsPaths(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := pngData(t)
	filenames := []string{
		"中文背景.png",
		"my background (final).png",
		"café 背景 🌌.png",
		"版本 (2).PNG",
	}
	var revision string
	for _, filename := range filenames {
		metadata, err := store.Import(bytes.NewReader(data), filename)
		if err != nil {
			t.Fatalf("Import(%q): %v", filename, err)
		}
		if metadata.Filename != filename {
			t.Fatalf("Import(%q) filename = %q", filename, metadata.Filename)
		}
		canonicalName := metadata.Revision + ".png"
		if metadata.URL != "/appearance/backgrounds/"+metadata.Revision+"/"+canonicalName {
			t.Fatalf("Import(%q) URL = %q", filename, metadata.URL)
		}
		for _, resourceName := range []string{canonicalName, filename} {
			resource, err := store.OpenResource(metadata.Revision, resourceName)
			if err != nil {
				t.Fatalf("OpenResource(%q): %v", resourceName, err)
			}
			opened, readErr := io.ReadAll(resource)
			resource.Close()
			if readErr != nil || !bytes.Equal(opened, data) {
				t.Fatalf("OpenResource(%q) data mismatch: %v", resourceName, readErr)
			}
		}
		if revision == "" {
			revision = metadata.Revision
		} else if metadata.Revision != revision {
			t.Fatalf("same image bytes changed revision: %s != %s", metadata.Revision, revision)
		}
	}

	entries, err := os.ReadDir(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("stored entries = %d, want current pointer and one content-addressed object", len(entries))
	}
	stored := map[string]bool{}
	for _, entry := range entries {
		stored[entry.Name()] = true
	}
	if !stored[CurrentFilename] || !stored[revision+".png"] {
		t.Fatalf("stored filenames = %v", stored)
	}
	for _, filename := range filenames {
		if stored[filename] {
			t.Fatalf("untrusted original filename used as storage path: %q", filename)
		}
	}
}

func TestStoreRejectsUnsafeAndUnsupportedFilenames(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := pngData(t)
	for _, filename := range []string{
		"../escape.png",
		`..\escape.png`,
		"/tmp/escape.png",
		`C:\escape.png`,
		".hidden.png",
		"line\nbreak.png",
		"background.svg",
		"background.exe",
		"background.png.exe",
	} {
		if _, err := store.Import(bytes.NewReader(data), filename); err == nil {
			t.Errorf("Import(%q) unexpectedly succeeded", filename)
		}
	}
	if _, err := store.Import(bytes.NewReader(data), "mismatched.jpg"); err == nil {
		t.Fatal("accepted PNG content with a JPEG extension")
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
