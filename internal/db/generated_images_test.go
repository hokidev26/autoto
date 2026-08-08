package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedImagesCRUDAndMessageTransaction(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Images", "", t.TempDir(), "fake:image", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 64)
	message, err := store.AddMessageWithGeneratedImages(ctx, Message{AgentID: agent.ID, Role: "assistant", ContentText: "generated"}, []GeneratedImage{{
		GenerationID: "generation-1", StorageKey: "objects/aa/" + sha + ".png", SHA256: sha,
		Filename: "generated-1.png", ByteSize: 123, Width: 4, Height: 3, RevisedPrompt: "revised", OutputIndex: 0,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(message.GeneratedImages) != 1 || message.GeneratedImages[0].MessageID != message.ID || message.GeneratedImages[0].AgentID != agent.ID {
		t.Fatalf("unexpected generated image response: %+v", message.GeneratedImages)
	}
	asset := message.GeneratedImages[0]
	got, err := store.GetGeneratedImage(ctx, agent.ID, message.ID, asset.ID)
	if err != nil || got.StorageKey != asset.StorageKey || got.RevisedPrompt != "revised" {
		t.Fatalf("unexpected generated image: %+v err=%v", got, err)
	}
	if _, err := store.GetGeneratedImage(ctx, "wrong-agent", message.ID, asset.ID); err != sql.ErrNoRows {
		t.Fatalf("expected ownership mismatch to be not found, got %v", err)
	}
	listed, err := store.ListGeneratedImagesByMessage(ctx, agent.ID, message.ID)
	if err != nil || len(listed) != 1 || listed[0].ID != asset.ID {
		t.Fatalf("unexpected list: %+v err=%v", listed, err)
	}
	keys, err := store.ListReferencedGeneratedImageStorageKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keys[asset.StorageKey]; !ok || len(keys) != 1 {
		t.Fatalf("unexpected referenced keys: %+v", keys)
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil || len(messages) != 1 || len(messages[0].GeneratedImages) != 1 {
		t.Fatalf("expected message listing to include image metadata: %+v err=%v", messages, err)
	}
	if messages[0].GeneratedImages[0].StorageKey == "" {
		t.Fatal("database result must retain internal storage key")
	}
	if err := store.SetGeneratedImageStatus(ctx, agent.ID, message.ID, asset.ID, "unavailable"); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetGeneratedImage(ctx, agent.ID, message.ID, asset.ID)
	if err != nil || got.Status != "unavailable" {
		t.Fatalf("status update failed: %+v err=%v", got, err)
	}
	if err := store.DeleteGeneratedImage(ctx, agent.ID, message.ID, asset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetGeneratedImage(ctx, agent.ID, message.ID, asset.ID); err != sql.ErrNoRows {
		t.Fatalf("expected deleted image to be absent, got %v", err)
	}
}

func TestAddMessageWithGeneratedImagesRollsBackAtomically(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Images", "", t.TempDir(), "fake:image", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("b", 64)
	image := GeneratedImage{GenerationID: "same", StorageKey: "objects/bb/" + sha + ".png", SHA256: sha, Filename: "image.png", ByteSize: 10, Width: 1, Height: 1, OutputIndex: 0}
	if _, err := store.AddMessageWithGeneratedImages(ctx, Message{ID: "rolled-back", AgentID: agent.ID, Role: "assistant"}, []GeneratedImage{image, image}); err == nil {
		t.Fatal("expected duplicate generation output to fail")
	}
	var count int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_messages WHERE id = 'rolled-back'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("message survived failed generated image transaction")
	}
	updated, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MessageCount != 0 {
		t.Fatalf("message count changed after rollback: %d", updated.MessageCount)
	}
}

func TestGeneratedImageMigrationFromVersion45(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw := openRawDB(t, path)
	oldSchema := strings.TrimSuffix(schemaSQL, generatedImagesSchemaSQL)
	if _, err := raw.ExecContext(ctx, oldSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `PRAGMA user_version = 45`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if version := readUserVersion(t, ctx, store.DB()); version != CurrentDBVersion {
		t.Fatalf("expected version %d, got %d", CurrentDBVersion, version)
	}
	var table string
	if err := store.DB().QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'agent_message_generated_images'`).Scan(&table); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedImageMetadataSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProject(ctx, "Restart", "", t.TempDir(), "fake:image", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("c", 64)
	message, err := store.AddMessageWithGeneratedImages(ctx, Message{AgentID: agent.ID, Role: "assistant"}, []GeneratedImage{{GenerationID: "restart", StorageKey: "objects/cc/" + sha + ".png", SHA256: sha, Filename: "restart.png", ByteSize: 42, Width: 2, Height: 2, OutputIndex: 0}})
	if err != nil {
		t.Fatal(err)
	}
	assetID := message.GeneratedImages[0].ID
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	asset, err := store.GetGeneratedImage(ctx, agent.ID, message.ID, assetID)
	if err != nil || asset.StorageKey != "objects/cc/"+sha+".png" {
		t.Fatalf("metadata did not survive restart: %+v err=%v", asset, err)
	}
}

// Generated images are fetched for a whole page in one query and grouped in Go,
// so the grouping is what needs pinning: an image landing on the wrong message
// would show one turn's picture under another.
func TestMessagePageGroupsGeneratedImagesOntoTheirOwnMessage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Images", "", t.TempDir(), "fake:image", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}

	shaA := strings.Repeat("a", 64)
	shaB := strings.Repeat("b", 64)
	shaC := strings.Repeat("c", 64)
	// Two images on one message, so their output_index order can be checked too.
	first, err := store.AddMessageWithGeneratedImages(ctx, Message{AgentID: agent.ID, Role: "assistant", ContentText: "one"}, []GeneratedImage{
		{GenerationID: "g1", StorageKey: "objects/aa/" + shaA + ".png", SHA256: shaA, Filename: "one-b.png", ByteSize: 2, Width: 1, Height: 1, OutputIndex: 1},
		{GenerationID: "g1", StorageKey: "objects/bb/" + shaB + ".png", SHA256: shaB, Filename: "one-a.png", ByteSize: 2, Width: 1, Height: 1, OutputIndex: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	bare, err := store.AddMessage(ctx, Message{AgentID: agent.ID, Role: "assistant", ContentText: "two"})
	if err != nil {
		t.Fatal(err)
	}
	third, err := store.AddMessageWithGeneratedImages(ctx, Message{AgentID: agent.ID, Role: "assistant", ContentText: "three"}, []GeneratedImage{
		{GenerationID: "g2", StorageKey: "objects/cc/" + shaC + ".png", SHA256: shaC, Filename: "three.png", ByteSize: 2, Width: 1, Height: 1, OutputIndex: 0},
	})
	if err != nil {
		t.Fatal(err)
	}

	page, err := store.ListMessagesPage(ctx, agent.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string][]GeneratedImage, len(page.Messages))
	for _, message := range page.Messages {
		byID[message.ID] = message.GeneratedImages
	}
	got := byID[first.ID]
	if len(got) != 2 {
		t.Fatalf("first message lost its images: %+v", got)
	}
	// Ordered by output_index, not insertion order.
	if got[0].Filename != "one-a.png" || got[1].Filename != "one-b.png" {
		t.Fatalf("images are not in output_index order: %+v", got)
	}
	for _, image := range got {
		if image.MessageID != first.ID {
			t.Fatalf("image filed under the wrong message: %+v", image)
		}
	}
	if images := byID[bare.ID]; len(images) != 0 {
		t.Fatalf("a message with no images must stay empty, got %+v", images)
	}
	if images := byID[third.ID]; len(images) != 1 || images[0].Filename != "three.png" {
		t.Fatalf("third message lost its image: %+v", images)
	}
}
