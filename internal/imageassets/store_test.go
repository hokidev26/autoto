package imageassets

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPutPNGOpenAndDeduplicate(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := testPNG(t, 3, 2)
	first, err := store.PutPNG(data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PutPNG(data)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.HasPrefix(first.StorageKey, "objects/"+first.SHA256[:2]+"/") {
		t.Fatalf("unexpected content addressed assets: first=%+v second=%+v", first, second)
	}
	file, err := store.Open(first.StorageKey, first.Expected())
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("opened bytes differ from published PNG")
	}
	if info, err := os.Stat(filepath.Join(store.Root(), filepath.FromSlash(first.StorageKey))); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("expected regular object: info=%v err=%v", info, err)
	}
}

func TestPutPNGRejectsInvalidAndOversizedImages(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"not png":   []byte("not png"),
		"too large": append(testPNG(t, 1, 1), make([]byte, MaxPNGBytes)...),
		"wide":      testPNG(t, MaxDimension+1, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.PutPNG(data); !errors.Is(err, ErrInvalidPNG) {
				t.Fatalf("expected invalid PNG, got %v", err)
			}
		})
	}
}

func TestOpenRejectsKeysTamperingAndMetadataMismatch(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	asset, err := store.PutPNG(testPNG(t, 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"../secret.png", "/objects/aa/file.png", `C:\objects\aa\file.png`, "objects/aa/" + asset.SHA256 + ".png", "objects/" + asset.SHA256[:2] + "/../" + asset.SHA256 + ".png"} {
		if file, err := store.Open(key, asset.Expected()); err == nil {
			file.Close()
			t.Fatalf("expected key %q to be rejected", key)
		}
	}
	wrong := asset.Expected()
	wrong.Width++
	if file, err := store.Open(asset.StorageKey, wrong); !errors.Is(err, ErrUnavailable) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("expected metadata mismatch to be unavailable, got %v", err)
	}
	path := filepath.Join(store.Root(), filepath.FromSlash(asset.StorageKey))
	if err := os.WriteFile(path, testPNG(t, 1, 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if file, err := store.Open(asset.StorageKey, asset.Expected()); !errors.Is(err, ErrUnavailable) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("expected tampered object to be unavailable, got %v", err)
	}
}

func TestOpenRejectsSymlinkObject(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	asset, err := store.PutPNG(testPNG(t, 2, 2))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Root(), filepath.FromSlash(asset.StorageKey))
	target := filepath.Join(t.TempDir(), "target.png")
	if err := os.WriteFile(target, testPNG(t, 2, 2), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable on Windows: %v", err)
		}
		t.Fatal(err)
	}
	if file, err := store.Open(asset.StorageKey, asset.Expected()); !errors.Is(err, ErrUnavailable) {
		if file != nil {
			file.Close()
		}
		t.Fatalf("expected symlink to be unavailable, got %v", err)
	}
}

func TestCleanupStagingAndMarkAndSweep(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	referenced, err := store.PutPNG(testPNG(t, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := store.PutPNG(testPNG(t, 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, asset := range []Asset{referenced, orphan} {
		path := filepath.Join(store.Root(), filepath.FromSlash(asset.StorageKey))
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	staging := filepath.Join(store.stagingDir, "abandoned.png")
	if err := os.WriteFile(staging, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staging, old, old); err != nil {
		t.Fatal(err)
	}
	stagingReport, err := store.CleanupStaging(24 * time.Hour)
	if err != nil || len(stagingReport.Removed) != 1 {
		t.Fatalf("unexpected staging cleanup: report=%+v err=%v", stagingReport, err)
	}
	report, err := store.MarkAndSweep(map[string]struct{}{referenced.StorageKey: {}}, 24*time.Hour)
	if err != nil || len(report.Removed) != 1 || report.Removed[0] != orphan.StorageKey {
		t.Fatalf("unexpected object cleanup: report=%+v err=%v", report, err)
	}
	if _, err := os.Stat(filepath.Join(store.Root(), filepath.FromSlash(referenced.StorageKey))); err != nil {
		t.Fatalf("referenced object was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root(), filepath.FromSlash(orphan.StorageKey))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan object still exists: %v", err)
	}
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.NRGBA{R: 1, G: 2, B: 3, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
