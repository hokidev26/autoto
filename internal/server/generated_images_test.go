package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/imageassets"
)

func TestGeneratedImageRouteServesVerifiedPNGAndHEAD(t *testing.T) {
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
	published, err := assets.PutPNG(generatedImagePNG(t, 3, 2))
	if err != nil {
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProject(ctx, "Images", "", t.TempDir(), "fake:image", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.AddMessageWithGeneratedImages(ctx, db.Message{AgentID: agent.ID, Role: "assistant"}, []db.GeneratedImage{{
		GenerationID: "gen-1", StorageKey: published.StorageKey, SHA256: published.SHA256, MIMEType: "image/png",
		Filename: "../../unsafe\r\nname", ByteSize: published.ByteSize, Width: published.Width, Height: published.Height, OutputIndex: 0,
	}})
	if err != nil {
		t.Fatal(err)
	}
	asset := message.GeneratedImages[0]
	app := New(config.Config{}, store, nil, nil)
	app.SetGeneratedImageStore(assets)
	routes := app.Routes()
	url := "/api/agents/" + agent.ID + "/messages/" + message.ID + "/generated-images/" + asset.ID

	get := httptest.NewRecorder()
	routes.ServeHTTP(get, newTestRequest(http.MethodGet, url, nil))
	if get.Code != http.StatusOK || !bytes.Equal(get.Body.Bytes(), generatedImagePNG(t, 3, 2)) {
		t.Fatalf("unexpected generated image response: code=%d body=%q", get.Code, get.Body.Bytes())
	}
	for name, expected := range map[string]string{
		"Content-Type":                 "image/png",
		"X-Content-Type-Options":       "nosniff",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Cache-Control":                "private, max-age=31536000, immutable",
		"ETag":                         `"` + published.SHA256 + `"`,
	} {
		if got := get.Header().Get(name); got != expected {
			t.Fatalf("expected %s=%q, got %q", name, expected, got)
		}
	}
	if get.Header().Get("Content-Disposition") != "" {
		t.Fatal("inline reads should not force a content disposition")
	}

	head := httptest.NewRecorder()
	routes.ServeHTTP(head, newTestRequest(http.MethodHead, url, nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
		t.Fatalf("unexpected HEAD response: code=%d headers=%v body=%q", head.Code, head.Header(), head.Body.String())
	}

	download := httptest.NewRecorder()
	routes.ServeHTTP(download, newTestRequest(http.MethodGet, url+"?download=1", nil))
	disposition := download.Header().Get("Content-Disposition")
	if download.Code != http.StatusOK || !strings.HasPrefix(disposition, "attachment;") || strings.ContainsAny(disposition, "\r\n") || !strings.Contains(strings.ToLower(disposition), ".png") {
		t.Fatalf("unsafe download disposition %q", disposition)
	}

	cachedRequest := newTestRequest(http.MethodGet, url, nil)
	cachedRequest.Header.Set("If-None-Match", `"`+published.SHA256+`"`)
	cached := httptest.NewRecorder()
	routes.ServeHTTP(cached, cachedRequest)
	if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
		t.Fatalf("unexpected conditional response: code=%d body=%q", cached.Code, cached.Body.String())
	}
}

func TestGeneratedImageRouteHidesOwnershipAndUnavailableFiles(t *testing.T) {
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
	published, err := assets.PutPNG(generatedImagePNG(t, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	_, _, owner, err := store.CreateProject(ctx, "Owner", "", t.TempDir(), "fake:image", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	_, _, other, err := store.CreateProject(ctx, "Other", "", t.TempDir(), "fake:image", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.AddMessageWithGeneratedImages(ctx, db.Message{AgentID: owner.ID, Role: "assistant"}, []db.GeneratedImage{{GenerationID: "gen", StorageKey: published.StorageKey, SHA256: published.SHA256, Filename: "image.png", ByteSize: published.ByteSize, Width: published.Width, Height: published.Height, OutputIndex: 0}})
	if err != nil {
		t.Fatal(err)
	}
	asset := message.GeneratedImages[0]
	app := New(config.Config{}, store, nil, nil)
	app.SetGeneratedImageStore(assets)
	routes := app.Routes()

	for _, url := range []string{
		"/api/agents/" + other.ID + "/messages/" + message.ID + "/generated-images/" + asset.ID,
		"/api/agents/" + owner.ID + "/messages/wrong/generated-images/" + asset.ID,
		"/api/agents/" + owner.ID + "/messages/" + message.ID + "/generated-images/wrong",
	} {
		recorder := httptest.NewRecorder()
		routes.ServeHTTP(recorder, newTestRequest(http.MethodGet, url, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("expected ownership mismatch %q to return 404, got %d", url, recorder.Code)
		}
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE agent_message_generated_images SET width = width + 1 WHERE id = ?`, asset.ID); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	url := "/api/agents/" + owner.ID + "/messages/" + message.ID + "/generated-images/" + asset.ID
	routes.ServeHTTP(recorder, newTestRequest(http.MethodGet, url, nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected metadata mismatch to return 404, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGeneratedImageMetadataJSONDoesNotExposeStorageKey(t *testing.T) {
	encoded, err := json.Marshal(db.GeneratedImage{ID: "asset", StorageKey: "objects/aa/secret.png", Filename: "image.png"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "storage") || strings.Contains(string(encoded), "objects/") {
		t.Fatalf("storage key leaked through JSON: %s", encoded)
	}
}

func generatedImagePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
