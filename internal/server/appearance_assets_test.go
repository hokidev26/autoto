package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"autoto/internal/appearanceassets"
	"autoto/internal/config"
)

func TestAppearanceBackgroundRoutesAndHeaders(t *testing.T) {
	home := t.TempDir()
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}}, nil, nil, nil)
	initial := httptest.NewRecorder()
	app.Routes().ServeHTTP(initial, newTestRequest(http.MethodGet, "/api/appearance/background", nil))
	if initial.Code != http.StatusOK || initial.Body.String() != "{\"background\":null}\n" {
		t.Fatalf("initial response = %d %s", initial.Code, initial.Body.String())
	}

	body, contentType := appearanceMultipart(t, "hero.png", "", appearancePNG(t))
	unauthorized := newTestRequest(http.MethodPost, "/api/appearance/background", body)
	unauthorized.Header.Set("Content-Type", contentType)
	denied := httptest.NewRecorder()
	app.Routes().ServeHTTP(denied, unauthorized)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized upload = %d: %s", denied.Code, denied.Body.String())
	}

	displayName := "中文 背景 (最终).png"
	body, contentType = appearanceMultipart(t, "background-upload.png", displayName, appearancePNG(t))
	upload := newTestRequest(http.MethodPost, "/api/appearance/background", body)
	upload.Header.Set(localTokenHeader, app.localToken)
	upload.Header.Set("Content-Type", contentType)
	created := httptest.NewRecorder()
	app.Routes().ServeHTTP(created, upload)
	if created.Code != http.StatusCreated {
		t.Fatalf("upload = %d: %s", created.Code, created.Body.String())
	}
	var response appearanceBackgroundResponse
	if err := json.NewDecoder(created.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Background == nil || response.Background.Filename != displayName || len(response.Background.Revision) != 64 {
		t.Fatalf("upload metadata = %#v", response)
	}
	expectedURL := "/appearance/backgrounds/" + response.Background.Revision + "/" + response.Background.Revision + ".png"
	if response.Background.URL != expectedURL || strings.ContainsAny(response.Background.URL, "中文 ()") {
		t.Fatalf("upload URL = %q, want canonical ASCII URL %q", response.Background.URL, expectedURL)
	}

	read := newTestRequest(http.MethodGet, response.Background.URL, nil)
	read.Header.Set(localTokenHeader, app.localToken)
	resource := httptest.NewRecorder()
	app.Routes().ServeHTTP(resource, read)
	if resource.Code != http.StatusOK || resource.Header().Get("Content-Type") != "image/png" || resource.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" || resource.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Fatalf("resource response = %d headers=%v", resource.Code, resource.Header())
	}
	if !strings.HasPrefix(resource.Body.String(), "\x89PNG") {
		t.Fatal("resource did not return PNG bytes")
	}

	head := newTestRequest(http.MethodHead, response.Background.URL, nil)
	head.Header.Set(localTokenHeader, app.localToken)
	headResult := httptest.NewRecorder()
	app.Routes().ServeHTTP(headResult, head)
	if headResult.Code != http.StatusOK || headResult.Body.Len() != 0 {
		t.Fatalf("HEAD = %d body=%d", headResult.Code, headResult.Body.Len())
	}

	remove := newTestRequest(http.MethodDelete, "/api/appearance/background", nil)
	remove.Header.Set(localTokenHeader, app.localToken)
	deleted := httptest.NewRecorder()
	app.Routes().ServeHTTP(deleted, remove)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(home + "/appearance/backgrounds/current.json"); !os.IsNotExist(err) {
		t.Fatalf("current pointer remains: %v", err)
	}
}

func TestAppearanceBackgroundUploadFallsBackToMultipartFilename(t *testing.T) {
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: t.TempDir()}}, nil, nil, nil)
	filename := "fallback background (1).png"
	body, contentType := appearanceMultipart(t, filename, "   ", appearancePNG(t))
	upload := newTestRequest(http.MethodPost, "/api/appearance/background", body)
	upload.Header.Set(localTokenHeader, app.localToken)
	upload.Header.Set("Content-Type", contentType)
	created := httptest.NewRecorder()
	app.Routes().ServeHTTP(created, upload)
	if created.Code != http.StatusCreated {
		t.Fatalf("upload = %d: %s", created.Code, created.Body.String())
	}
	var response appearanceBackgroundResponse
	if err := json.NewDecoder(created.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Background == nil || response.Background.Filename != filename {
		t.Fatalf("upload metadata = %#v", response)
	}
	expectedURL := "/appearance/backgrounds/" + response.Background.Revision + "/" + response.Background.Revision + ".png"
	if response.Background.URL != expectedURL {
		t.Fatalf("upload URL = %q, want %q", response.Background.URL, expectedURL)
	}
}

func appearanceMultipart(t *testing.T, filename, displayName string, data []byte) (*bytes.Reader, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if displayName != "" {
		if err := writer.WriteField("displayName", displayName); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(body.Bytes()), writer.FormDataContentType()
}

func appearancePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.Set(x, y, color.RGBA{R: 0xaa, G: 0x15, B: 0x1b, A: 0xff})
		}
	}
	var body bytes.Buffer
	if err := png.Encode(&body, img); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func TestRestrictedRemoteAppearanceBackgroundIsReadOnly(t *testing.T) {
	app := remoteAccessTestServer(t)
	store, err := appearanceassets.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	background, err := store.Import(bytes.NewReader(appearancePNG(t)), "remote.png")
	if err != nil {
		t.Fatal(err)
	}
	app.SetAppearanceAssetStore(store)
	cookies := loginRemoteAccess(t, app, remoteAccessModeRestricted)

	read := newTestRequest(http.MethodGet, "/api/appearance/background", nil)
	read.Host = "remote.example.test"
	markRemoteHTTPS(read)
	for _, cookie := range cookies {
		read.AddCookie(cookie)
	}
	readResult := httptest.NewRecorder()
	app.Routes().ServeHTTP(readResult, read)
	if readResult.Code != http.StatusOK || !strings.Contains(readResult.Body.String(), background.Revision) {
		t.Fatalf("restricted remote read = %d: %s", readResult.Code, readResult.Body.String())
	}

	resource := newTestRequest(http.MethodGet, background.URL, nil)
	resource.Host = "remote.example.test"
	markRemoteHTTPS(resource)
	for _, cookie := range cookies {
		resource.AddCookie(cookie)
	}
	resourceResult := httptest.NewRecorder()
	app.Routes().ServeHTTP(resourceResult, resource)
	if resourceResult.Code != http.StatusOK {
		t.Fatalf("restricted remote resource = %d: %s", resourceResult.Code, resourceResult.Body.String())
	}

	body, contentType := appearanceMultipart(t, "new.png", "", appearancePNG(t))
	upload := newTestRequest(http.MethodPost, "/api/appearance/background", body)
	upload.Host = "remote.example.test"
	markRemoteHTTPS(upload)
	upload.Header.Set("Content-Type", contentType)
	upload.Header.Set(localTokenHeader, app.localToken)
	for _, cookie := range cookies {
		upload.AddCookie(cookie)
	}
	uploadResult := httptest.NewRecorder()
	app.Routes().ServeHTTP(uploadResult, upload)
	if uploadResult.Code != http.StatusForbidden {
		t.Fatalf("restricted remote upload = %d: %s", uploadResult.Code, uploadResult.Body.String())
	}
}
