package themes

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestManifestStrictValidation(t *testing.T) {
	manifest := testManifest()
	encoded := mustManifestJSON(t, manifest)
	parsed, err := ParseManifest(encoded)
	if err != nil {
		t.Fatalf("ParseManifest() error = %v", err)
	}
	if parsed.ID != manifest.ID || parsed.Tokens.Primary != manifest.Tokens.Primary {
		t.Fatalf("parsed manifest = %#v", parsed)
	}

	unknown := bytes.Replace(encoded, []byte(`"name":`), []byte(`"unknown":true,"name":`), 1)
	if _, err := ParseManifest(unknown); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
	if _, err := ParseManifest(append(encoded, []byte(` {}`)...)); err == nil {
		t.Fatal("ParseManifest accepted trailing JSON")
	}
	duplicate := bytes.Replace(encoded, []byte(`"name":`), []byte(`"name":"Duplicate","name":`), 1)
	if _, err := ParseManifest(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate JSON field") {
		t.Fatalf("duplicate field error = %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"schema", func(value *Manifest) { value.SchemaVersion = 2 }},
		{"id", func(value *Manifest) { value.ID = "Bad/Theme" }},
		{"color", func(value *Manifest) { value.Tokens.Primary = "url(https://example.test/x)" }},
		{"scheme", func(value *Manifest) { value.ColorScheme = "system" }},
		{"material kind", func(value *Manifest) { value.Materials.Card.Kind = "css" }},
		{"opacity", func(value *Manifest) { value.Materials.Card.Opacity = 1.1 }},
		{"blur", func(value *Manifest) { value.Materials.Card.Blur = 65 }},
		{"scope", func(value *Manifest) { value.HomeBackground.Scope = "global" }},
		{"resource traversal", func(value *Manifest) { value.Preview = "../preview.png" }},
		{"resource URL", func(value *Manifest) { value.Preview = "https://example.test/preview.png" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value := testManifest()
			test.mutate(&value)
			if err := ValidateManifest(value); err == nil {
				t.Fatalf("ValidateManifest(%s) unexpectedly succeeded", test.name)
			}
		})
	}
}

func TestPNGAndJPEGDimensionLimits(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		data []byte
	}{
		{"PNG dimension", "wide.png", testPNGConfig(MaxImageDimension+1, 1)},
		{"PNG pixels", "pixels.png", testPNGConfig(6000, 6000)},
		{"JPEG dimension", "wide.jpg", testJPEGConfig(MaxImageDimension+1, 1)},
		{"JPEG pixels", "pixels.jpeg", testJPEGConfig(6000, 6000)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateImageBytes(test.path, test.data); err == nil {
				t.Fatalf("validateImageBytes accepted unsafe dimensions for %s", test.path)
			}
		})
	}
}

func TestWebPHeaderValidation(t *testing.T) {
	valid := testWebPVP8L(64, 32)
	contentType, err := validateImageBytes("preview.webp", valid)
	if err != nil || contentType != "image/webp" {
		t.Fatalf("valid WebP = %q, %v", contentType, err)
	}
	if _, err := validateImageBytes("preview.webp", testWebPVP8L(MaxImageDimension+1, 1)); err == nil {
		t.Fatal("validateImageBytes accepted excessive WebP dimensions")
	}
	truncated := append([]byte(nil), valid[:len(valid)-1]...)
	if _, err := validateImageBytes("preview.webp", truncated); err == nil {
		t.Fatal("validateImageBytes accepted truncated WebP")
	}
}

func TestArchiveSafetyRejections(t *testing.T) {
	manifest := testManifest()
	validManifest := mustManifestJSON(t, manifest)
	pngData := testPNG(t, color.RGBA{R: 0x75, G: 0xaa, B: 0xdb, A: 0xff})

	cases := []struct {
		name    string
		entries []zipTestEntry
	}{
		{"zip slip", []zipTestEntry{{name: ManifestFilename, data: validManifest}, {name: "../preview.png", data: pngData}}},
		{"absolute", []zipTestEntry{{name: ManifestFilename, data: validManifest}, {name: "/preview.png", data: pngData}}},
		{"backslash", []zipTestEntry{{name: ManifestFilename, data: validManifest}, {name: `assets\preview.png`, data: pngData}}},
		{"dot path", []zipTestEntry{{name: ManifestFilename, data: validManifest}, {name: "assets/./preview.png", data: pngData}}},
		{"hidden", []zipTestEntry{{name: ManifestFilename, data: validManifest}, {name: "assets/.preview.png", data: pngData}}},
		{"symlink", []zipTestEntry{{name: ManifestFilename, data: validManifest}, {name: manifest.Preview, data: pngData, mode: os.ModeSymlink | 0o777}}},
		{"undeclared", []zipTestEntry{{name: ManifestFilename, data: validManifest}, {name: manifest.Preview, data: pngData}, {name: "theme.css", data: []byte("body{}")}}},
		{"wrong MIME", []zipTestEntry{{name: ManifestFilename, data: validManifest}, {name: manifest.Preview, data: []byte("not a png")}, {name: manifest.HomeBackground.Path, data: pngData}}},
		{"truncated PNG", []zipTestEntry{{name: ManifestFilename, data: validManifest}, {name: manifest.Preview, data: []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}}, {name: manifest.HomeBackground.Path, data: pngData}}},
		{"excessive dimensions", []zipTestEntry{{name: ManifestFilename, data: validManifest}, {name: manifest.Preview, data: testPNGSize(t, MaxImageDimension+1, 1, color.Black)}, {name: manifest.HomeBackground.Path, data: pngData}}},
		{"duplicate normalized", []zipTestEntry{{name: ManifestFilename, data: validManifest}, {name: manifest.Preview, data: pngData}, {name: strings.ToUpper(manifest.Preview), data: pngData}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			_, err := store.Import(bytes.NewReader(makeArchive(t, test.entries)), ImportOptions{})
			if !errors.Is(err, ErrInvalidArchive) {
				t.Fatalf("Import() error = %v, want ErrInvalidArchive", err)
			}
		})
	}
}

func TestArchiveBounds(t *testing.T) {
	store := newTestStore(t)
	oversized := bytes.Repeat([]byte("x"), MaxArchiveBytes+1)
	if _, err := store.Import(bytes.NewReader(oversized), ImportOptions{}); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("oversized upload error = %v", err)
	}

	manifest := testManifest()
	entries := []zipTestEntry{{name: ManifestFilename, data: mustManifestJSON(t, manifest)}}
	entries = append(entries, zipTestEntry{name: manifest.Preview, data: bytes.Repeat([]byte("x"), MaxImageBytes+1)})
	entries = append(entries, zipTestEntry{name: manifest.HomeBackground.Path, data: testPNG(t, color.White)})
	if _, err := newTestStore(t).Import(bytes.NewReader(makeArchive(t, entries)), ImportOptions{}); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("oversized image error = %v", err)
	}

	many := []zipTestEntry{{name: ManifestFilename, data: mustManifestJSON(t, manifest)}}
	for index := 0; index < MaxArchiveEntries; index++ {
		many = append(many, zipTestEntry{name: "junk/file" + string(rune('a'+index%26)) + ".png", data: testPNG(t, color.Black)})
	}
	if _, err := newTestStore(t).Import(bytes.NewReader(makeArchive(t, many)), ImportOptions{}); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("entry count error = %v", err)
	}
}

func TestStoreImportReplaceRevisionResourceAndDelete(t *testing.T) {
	store := newTestStore(t)
	manifest := testManifest()
	firstPNG := testPNG(t, color.RGBA{R: 0x75, G: 0xaa, B: 0xdb, A: 0xff})
	background := testPNG(t, color.RGBA{R: 0xf1, G: 0xbf, B: 0x00, A: 0xff})
	firstArchive := themeArchive(t, manifest, firstPNG, background, []byte("Original test license\n"))

	first, err := store.Import(bytes.NewReader(firstArchive), ImportOptions{})
	if err != nil {
		t.Fatalf("Import(first) error = %v", err)
	}
	if first.Bundled || first.Source != SourceLocal || !first.Deletable || first.ID != manifest.ID || !validRevision(first.Revision) || len(first.Resources) != 2 {
		t.Fatalf("first theme metadata = %#v", first)
	}
	if first.StylesheetURL != "/themes/"+manifest.ID+"/"+first.Revision+"/theme.css" || first.PreviewURL != "/themes/"+manifest.ID+"/"+first.Revision+"/"+manifest.Preview {
		t.Fatalf("handler URLs = stylesheet %q preview %q", first.StylesheetURL, first.PreviewURL)
	}
	metadataJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(metadataJSON, []byte(`"manifest"`)) || !bytes.Contains(metadataJSON, []byte(`"source":"local"`)) {
		t.Fatalf("flat theme JSON = %s", metadataJSON)
	}
	if _, err := store.Import(bytes.NewReader(firstArchive), ImportOptions{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate import error = %v", err)
	}

	resource, err := store.OpenResource(manifest.ID, first.Revision, manifest.Preview)
	if err != nil {
		t.Fatalf("OpenResource() error = %v", err)
	}
	opened, err := io.ReadAll(resource)
	resource.Close()
	if err != nil || !bytes.Equal(opened, firstPNG) || resource.Metadata.ContentType != "image/png" {
		t.Fatalf("opened resource mismatch: err=%v metadata=%#v", err, resource.Metadata)
	}
	if _, err := store.OpenResource(manifest.ID, first.Revision, "undeclared.png"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("undeclared resource error = %v", err)
	}

	secondPNG := testPNG(t, color.RGBA{R: 0xaa, G: 0x15, B: 0x1b, A: 0xff})
	secondArchive := themeArchive(t, manifest, secondPNG, background, []byte("Original test license\n"))
	second, err := store.Import(bytes.NewReader(secondArchive), ImportOptions{Replace: true})
	if err != nil {
		t.Fatalf("Import(replace) error = %v", err)
	}
	if second.Revision == first.Revision {
		t.Fatal("content change did not change revision")
	}
	current, err := store.Get(manifest.ID)
	if err != nil || current.Revision != second.Revision {
		t.Fatalf("Get() = %#v, %v", current, err)
	}
	old, err := store.OpenResource(manifest.ID, first.Revision, manifest.Preview)
	if err != nil {
		t.Fatalf("old revision should remain content-addressable: %v", err)
	}
	old.Close()
	oldCSS, err := store.CSSForRevision(manifest.ID, first.Revision)
	if err != nil || !strings.Contains(oldCSS, "/themes/"+manifest.ID+"/"+first.Revision+"/"+manifest.HomeBackground.Path) {
		t.Fatalf("CSSForRevision(old) error = %v, CSS = %s", err, oldCSS)
	}

	if err := store.Delete(manifest.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get(manifest.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(deleted) error = %v", err)
	}
	if _, err := store.OpenResource(manifest.ID, second.Revision, manifest.Preview); !errors.Is(err, ErrNotFound) {
		t.Fatalf("OpenResource(deleted) error = %v", err)
	}
}

func TestRevisionStableAndTamperingFailsClosed(t *testing.T) {
	manifest := testManifest()
	archive := themeArchive(t, manifest, testPNG(t, color.RGBA{B: 0xff, A: 0xff}), testPNG(t, color.RGBA{R: 0xff, G: 0xff, A: 0xff}), nil)
	first, err := newTestStore(t).Import(bytes.NewReader(archive), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secondStore := newTestStore(t)
	second, err := secondStore.Import(bytes.NewReader(archive), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != second.Revision {
		t.Fatalf("stable revisions differ: %s != %s", first.Revision, second.Revision)
	}

	resourcePath := filepath.Join(secondStore.Root(), manifest.ID, second.Revision, filepath.FromSlash(manifest.Preview))
	if err := os.WriteFile(resourcePath, testPNG(t, color.RGBA{R: 0xff, A: 0xff}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := secondStore.OpenResource(manifest.ID, second.Revision, manifest.Preview); !errors.Is(err, ErrRevisionMismatch) {
		t.Fatalf("tampered OpenResource() error = %v", err)
	}
	if _, err := secondStore.Get(manifest.ID); !errors.Is(err, ErrRevisionMismatch) {
		t.Fatalf("tampered Get() error = %v", err)
	}
}

func TestResourceSymlinkRejectedAtOpen(t *testing.T) {
	store := newTestStore(t)
	manifest := testManifest()
	theme, err := store.Import(bytes.NewReader(themeArchive(t, manifest, testPNG(t, color.RGBA{B: 0xff, A: 0xff}), testPNG(t, color.RGBA{R: 0xff, G: 0xff, A: 0xff}), nil)), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resourcePath := filepath.Join(store.Root(), manifest.ID, theme.Revision, filepath.FromSlash(manifest.Preview))
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, testPNG(t, color.RGBA{R: 0xff, A: 0xff}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(resourcePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, resourcePath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.OpenResource(manifest.ID, theme.Revision, manifest.Preview); err == nil {
		t.Fatal("OpenResource accepted a symlink")
	}
}

func TestThemeDirectorySymlinkRejectedAtOpen(t *testing.T) {
	store := newTestStore(t)
	manifest := testManifest()
	theme, err := store.Import(bytes.NewReader(themeArchive(t, manifest, testPNG(t, color.RGBA{B: 0xff, A: 0xff}), testPNG(t, color.RGBA{R: 0xff, G: 0xff, A: 0xff}), nil)), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	themeDir := filepath.Join(store.Root(), manifest.ID)
	outsideDir := filepath.Join(t.TempDir(), manifest.ID)
	if err := os.Rename(themeDir, outsideDir); err != nil {
		t.Skipf("cannot move theme directory for symlink test: %v", err)
	}
	if err := os.Symlink(outsideDir, themeDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := store.OpenResource(manifest.ID, theme.Revision, manifest.Preview); err == nil {
		t.Fatal("OpenResource accepted a symlinked theme directory")
	}
}

func TestBundledThemeProtectedAndCSSScoped(t *testing.T) {
	store := newTestStore(t)
	listed, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	var bundled Theme
	for _, theme := range listed {
		if theme.Manifest.ID == "argentina-spain-final" {
			bundled = theme
		}
	}
	if !bundled.Bundled || bundled.Source != SourceBundled || bundled.Deletable || !validRevision(bundled.Revision) {
		t.Fatalf("bundled theme missing: %#v", bundled)
	}
	if ratio := contrastRatio(bundled.Manifest.Tokens.Text, bundled.Manifest.Tokens.Card); ratio < 4.5 {
		t.Fatalf("bundled text/card contrast = %.2f, want >= 4.5", ratio)
	}
	if ratio := contrastRatio(bundled.Manifest.Tokens.Text, bundled.Manifest.Tokens.Input); ratio < 4.5 {
		t.Fatalf("bundled text/input contrast = %.2f, want >= 4.5", ratio)
	}
	if err := store.Delete(bundled.Manifest.ID); !errors.Is(err, ErrBundledProtected) {
		t.Fatalf("Delete(bundled) error = %v", err)
	}
	archive := themeArchive(t, bundled.Manifest, nil, nil, nil)
	if _, err := store.Import(bytes.NewReader(archive), ImportOptions{Replace: true}); !errors.Is(err, ErrBundledProtected) {
		t.Fatalf("Import(bundled replace) error = %v", err)
	}
	css, err := GenerateCSS(bundled)
	if err != nil {
		t.Fatal(err)
	}
	selector := `body.white-shell[data-autoto-theme="argentina-spain-final"]`
	if !strings.Contains(css, selector) || strings.Contains(css, "http://") || strings.Contains(css, "https://") || strings.Contains(css, "@import") {
		t.Fatalf("unsafe or incorrectly scoped CSS:\n%s", css)
	}
	if !strings.Contains(css, "--autoto-theme-home-image: radial-gradient") || !strings.Contains(css, "--autoto-accent-gradient") {
		t.Fatalf("bundled CSS lacks generated atmosphere variables:\n%s", css)
	}
	for _, variable := range []string{
		"--ws-canvas:", "--ws-sidebar:", "--ws-card:", "--ws-input:", "--ws-text:", "--ws-muted:",
		"--ws-border:", "--ws-primary:", "--autoto-theme-secondary:", "--autoto-theme-danger:",
		"--autoto-theme-terminal:", "--autoto-theme-message-user:", "--autoto-theme-surface-opacity:",
		"--autoto-theme-blur:", "--autoto-theme-radius:", "--autoto-theme-accent-text:",
		"--autoto-theme-home-overlay:", "--autoto-theme-home-position:",
	} {
		if !strings.Contains(css, variable) {
			t.Errorf("bundled CSS missing %s", variable)
		}
	}
	if !strings.Contains(css, "--autoto-theme-accent-text: #000000;") || !strings.Contains(css, "--autoto-accent-gradient: linear-gradient") {
		t.Fatalf("bundled accent accessibility recipe is unexpected:\n%s", css)
	}
}

func TestCSSAccentFallsBackWhenGradientHasNoSharedForeground(t *testing.T) {
	manifest := testManifest()
	manifest.Tokens.Primary = "#777777"
	manifest.Tokens.Secondary = "#0000FF"
	revision, err := hashThemeContent(manifest, map[string][]byte{
		manifest.Preview:             testPNG(t, color.Black),
		manifest.HomeBackground.Path: testPNG(t, color.White),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	css, err := GenerateCSS(newThemeMetadata(manifest, revision, false, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(css, "--autoto-theme-accent-text: #000000;") || !strings.Contains(css, "--autoto-accent-gradient: var(--autoto-color-primary);") {
		t.Fatalf("unsafe mixed-endpoint gradient was not downgraded:\n%s", css)
	}
}

func TestCSSUsesRevisionedLocalResourceURL(t *testing.T) {
	store := newTestStore(t)
	manifest := testManifest()
	theme, err := store.Import(bytes.NewReader(themeArchive(t, manifest, testPNG(t, color.RGBA{B: 0xff, A: 0xff}), testPNG(t, color.RGBA{R: 0xff, G: 0xff, A: 0xff}), nil)), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	css, err := store.CSS(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := `/themes/` + manifest.ID + `/` + theme.Revision + `/` + manifest.HomeBackground.Path
	if !strings.Contains(css, want) || strings.Contains(css, "http://") || strings.Contains(css, "https://") {
		t.Fatalf("CSS resource URL mismatch:\n%s", css)
	}
}

func TestPrivatePermissions(t *testing.T) {
	store := newTestStore(t)
	manifest := testManifest()
	theme, err := store.Import(bytes.NewReader(themeArchive(t, manifest, testPNG(t, color.RGBA{B: 0xff, A: 0xff}), testPNG(t, color.RGBA{R: 0xff, G: 0xff, A: 0xff}), nil)), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for _, item := range []struct {
			path string
			mode os.FileMode
		}{
			{store.Root(), 0o700},
			{filepath.Join(store.Root(), manifest.ID), 0o700},
			{filepath.Join(store.Root(), manifest.ID, theme.Revision), 0o700},
			{filepath.Join(store.Root(), manifest.ID, "current"), 0o600},
			{filepath.Join(store.Root(), manifest.ID, theme.Revision, ManifestFilename), 0o600},
			{filepath.Join(store.Root(), manifest.ID, theme.Revision, filepath.FromSlash(manifest.Preview)), 0o600},
		} {
			info, err := os.Stat(item.path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != item.mode {
				t.Errorf("%s mode = %o, want %o", item.path, info.Mode().Perm(), item.mode)
			}
		}
	}
}

type zipTestEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testManifest() Manifest {
	return Manifest{
		SchemaVersion: SchemaVersionV1,
		ID:            "test-theme", Name: "Test Theme", Version: "1.0.0",
		Description: "A controlled test theme.", Author: "Autoto Tests", ColorScheme: ColorSchemeDark,
		Tokens: Tokens{
			Canvas: "#07111F", Sidebar: "#0A1C30", Card: "#F6FAFF", Input: "#E8F3FC",
			Text: "#F7FBFF", Muted: "#9DB1C8", Border: "#75AADB", Primary: "#75AADB",
			Secondary: "#F1BF00", Danger: "#AA151B", Terminal: "#090A0C", Message: "#132B47",
		},
		Materials: Materials{
			Canvas: material(MaterialSolid), Sidebar: material(MaterialGlass), Card: material(MaterialTranslucent),
			Input: material(MaterialTranslucent), Terminal: material(MaterialSolid), Message: material(MaterialGlass),
		},
		Preview:        "assets/preview.png",
		HomeBackground: &HomeBackground{Path: "assets/home.png", Scope: "home"},
	}
}

func material(kind string) Material {
	return Material{Kind: kind, Opacity: 0.9, Blur: 8, Radius: 12, Shadow: ShadowSoft}
}

func mustManifestJSON(t *testing.T, manifest Manifest) []byte {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func themeArchive(t *testing.T, manifest Manifest, preview, background, license []byte) []byte {
	t.Helper()
	entries := []zipTestEntry{{name: ManifestFilename, data: mustManifestJSON(t, manifest)}}
	if manifest.Preview != "" {
		entries = append(entries, zipTestEntry{name: manifest.Preview, data: preview})
	}
	if manifest.HomeBackground != nil {
		entries = append(entries, zipTestEntry{name: manifest.HomeBackground.Path, data: background})
	}
	if license != nil {
		entries = append(entries, zipTestEntry{name: LicenseFilename, data: license})
	}
	return makeArchive(t, entries)
}

func makeArchive(t *testing.T, entries []zipTestEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		part, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func testPNG(t *testing.T, fill color.Color) []byte {
	t.Helper()
	return testPNGSize(t, 2, 2, fill)
}

func testPNGSize(t *testing.T, width, height int, fill color.Color) []byte {
	t.Helper()
	imageData := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			imageData.Set(x, y, fill)
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, imageData); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func testPNGConfig(width, height int) []byte {
	data := make([]byte, 8+12+13)
	copy(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	binary.BigEndian.PutUint32(data[8:12], 13)
	copy(data[12:16], "IHDR")
	binary.BigEndian.PutUint32(data[16:20], uint32(width))
	binary.BigEndian.PutUint32(data[20:24], uint32(height))
	data[24] = 8
	data[25] = 2
	crc := crc32.ChecksumIEEE(data[12:29])
	crcBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(crcBytes, crc)
	return append(data, crcBytes...)
}

func testJPEGConfig(width, height int) []byte {
	data := []byte{
		0xff, 0xd8,
		0xff, 0xc0, 0x00, 0x11, 0x08,
		byte(height >> 8), byte(height), byte(width >> 8), byte(width), 0x03,
		0x01, 0x11, 0x00, 0x02, 0x11, 0x00, 0x03, 0x11, 0x00,
		0xff, 0xd9,
	}
	return data
}

func testWebPVP8L(width, height int) []byte {
	bits := uint32(width-1) | uint32(height-1)<<14
	payload := make([]byte, 5)
	payload[0] = 0x2f
	binary.LittleEndian.PutUint32(payload[1:], bits)
	total := 12 + 8 + len(payload) + 1
	data := make([]byte, total)
	copy(data[:4], "RIFF")
	binary.LittleEndian.PutUint32(data[4:8], uint32(total-8))
	copy(data[8:12], "WEBP")
	copy(data[12:16], "VP8L")
	binary.LittleEndian.PutUint32(data[16:20], uint32(len(payload)))
	copy(data[20:], payload)
	return data
}

func contrastRatio(foreground, background string) float64 {
	first := relativeLuminance(foreground)
	second := relativeLuminance(background)
	if first < second {
		first, second = second, first
	}
	return (first + 0.05) / (second + 0.05)
}

func relativeLuminance(value string) float64 {
	channels := make([]float64, 3)
	for index := range channels {
		parsed, err := strconv.ParseUint(value[1+index*2:3+index*2], 16, 8)
		if err != nil {
			return 0
		}
		channel := float64(parsed) / 255
		if channel <= 0.04045 {
			channels[index] = channel / 12.92
		} else {
			channels[index] = math.Pow((channel+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*channels[0] + 0.7152*channels[1] + 0.0722*channels[2]
}
