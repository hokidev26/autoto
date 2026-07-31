package app

import (
	"bytes"
	"context"
	"errors"
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

func TestGeneratedImagesCleanupServiceStartsWithoutWaitingForCleanup(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	service := &generatedImagesCleanupService{cleanup: func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
	}}

	startResult := make(chan error, 1)
	go func() { startResult <- service.Start(context.Background()) }()
	select {
	case err := <-startResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup service Start blocked on cleanup")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cleanup service did not execute")
	}

	closeResult := make(chan error, 1)
	go func() { closeResult <- service.Close(context.Background()) }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel cleanup")
	}
	select {
	case err := <-closeResult:
		t.Fatalf("Close returned before cleanup finished: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not wait for cleanup")
	}
}

func TestGeneratedImagesCleanupServiceCloseHonorsContext(t *testing.T) {
	release := make(chan struct{})
	service := &generatedImagesCleanupService{cleanup: func(ctx context.Context) {
		<-ctx.Done()
		<-release
	}}
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := service.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want deadline exceeded", err)
	}
	close(release)
	if err := service.Close(context.Background()); err != nil {
		t.Fatalf("Close() after worker release = %v", err)
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
