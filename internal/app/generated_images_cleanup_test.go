package app

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"autoto/internal/db"
	"autoto/internal/imageassets"
)

func TestCleanupGeneratedImagesUsesDatabaseReferences(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	store, err := db.Open(ctx, filepath.Join(home, "autoto.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assets, err := imageassets.New(home)
	if err != nil {
		t.Fatal(err)
	}
	referenced, err := assets.PutPNG(appTestPNG(t, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := assets.PutPNG(appTestPNG(t, 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProject(ctx, "Cleanup", "", t.TempDir(), "fake:image", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessageWithGeneratedImages(ctx, db.Message{AgentID: agent.ID, Role: "assistant"}, []db.GeneratedImage{{GenerationID: "keep", StorageKey: referenced.StorageKey, SHA256: referenced.SHA256, Filename: "keep.png", ByteSize: referenced.ByteSize, Width: referenced.Width, Height: referenced.Height, OutputIndex: 0}}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * generatedImageCleanupGrace)
	for _, asset := range []imageassets.Asset{referenced, orphan} {
		path := filepath.Join(assets.Root(), filepath.FromSlash(asset.StorageKey))
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	staging := filepath.Join(assets.Root(), "staging", "abandoned.png")
	if err := os.WriteFile(staging, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staging, old, old); err != nil {
		t.Fatal(err)
	}
	cleanupGeneratedImages(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), store, assets)
	if _, err := os.Stat(filepath.Join(assets.Root(), filepath.FromSlash(referenced.StorageKey))); err != nil {
		t.Fatalf("referenced object was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(assets.Root(), filepath.FromSlash(orphan.StorageKey))); !os.IsNotExist(err) {
		t.Fatalf("orphan object was not removed: %v", err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging file was not removed: %v", err)
	}
}

func appTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewNRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
