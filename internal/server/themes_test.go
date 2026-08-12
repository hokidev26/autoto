package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/themes"
)

func TestThemeRoutesListAndProtectRevisionedStyles(t *testing.T) {
	home := t.TempDir()
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}}, nil, nil, nil)

	listRequest := newTestRequest(http.MethodGet, "/api/themes", nil)
	listed := httptest.NewRecorder()
	app.Routes().ServeHTTP(listed, listRequest)
	if listed.Code != http.StatusOK {
		t.Fatalf("theme list returned %d: %s", listed.Code, listed.Body.String())
	}
	var response themeListResponse
	if err := json.NewDecoder(listed.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Themes) == 0 {
		t.Fatal("expected at least one bundled theme")
	}
	var bundled themes.Theme
	for _, theme := range response.Themes {
		if theme.ID == "argentina-spain-final" {
			bundled = theme
			break
		}
	}
	if bundled.ID == "" || bundled.Source != themes.SourceBundled || bundled.Deletable || bundled.StylesheetURL == "" || bundled.PreviewURL == "" {
		t.Fatalf("unexpected bundled theme metadata: %+v", bundled)
	}
	if bundled.Version != "2.0.0" || bundled.Capabilities != (themes.ThemeCapabilities{GlobalBackground: true, HomeBackground: true, Icons: true}) || len(bundled.Resources) != len(themes.AllowedIconSlots)+3 {
		t.Fatalf("bundled theme assets are incomplete: %+v", bundled)
	}

	noCookie := httptest.NewRecorder()
	app.Routes().ServeHTTP(noCookie, newTestRequest(http.MethodGet, bundled.StylesheetURL, nil))
	if noCookie.Code != http.StatusUnauthorized {
		t.Fatalf("theme stylesheet without cookie returned %d: %s", noCookie.Code, noCookie.Body.String())
	}

	styleRequest := newTestRequest(http.MethodGet, bundled.StylesheetURL, nil)
	styleRequest.AddCookie(&http.Cookie{Name: localTokenCookieName, Value: app.localToken})
	stylesheet := httptest.NewRecorder()
	app.Routes().ServeHTTP(stylesheet, styleRequest)
	if stylesheet.Code != http.StatusOK {
		t.Fatalf("theme stylesheet returned %d: %s", stylesheet.Code, stylesheet.Body.String())
	}
	css := stylesheet.Body.String()
	for _, expected := range []string{
		`body.white-shell[data-autoto-theme="argentina-spain-final"]`,
		"--ws-canvas:",
		"--ws-card:",
		"--autoto-theme-home-image:",
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("generated stylesheet is missing %q:\n%s", expected, css)
		}
	}
	if got := stylesheet.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Fatalf("unexpected resource policy %q", got)
	}
	if got := stylesheet.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("unexpected nosniff header %q", got)
	}

	previewRequest := newTestRequest(http.MethodGet, bundled.PreviewURL, nil)
	previewRequest.AddCookie(&http.Cookie{Name: localTokenCookieName, Value: app.localToken})
	preview := httptest.NewRecorder()
	app.Routes().ServeHTTP(preview, previewRequest)
	if preview.Code != http.StatusOK || preview.Header().Get("Content-Type") != "image/png" || preview.Body.Len() == 0 {
		t.Fatalf("bundled preview returned %d %q (%d bytes)", preview.Code, preview.Header().Get("Content-Type"), preview.Body.Len())
	}
	if got := preview.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("unexpected preview cache policy %q", got)
	}

	crossSiteRequest := newTestRequest(http.MethodGet, bundled.StylesheetURL, nil)
	crossSiteRequest.Header.Set("Sec-Fetch-Site", "cross-site")
	crossSiteRequest.AddCookie(&http.Cookie{Name: localTokenCookieName, Value: app.localToken})
	crossSite := httptest.NewRecorder()
	app.Routes().ServeHTTP(crossSite, crossSiteRequest)
	if crossSite.Code != http.StatusForbidden {
		t.Fatalf("cross-site theme stylesheet returned %d: %s", crossSite.Code, crossSite.Body.String())
	}
}

func TestThemeRoutesImportReplaceAndDeleteLocalTheme(t *testing.T) {
	home := t.TempDir()
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}}, nil, nil, nil)
	manifest := serverThemeManifest("local-match-night")
	archive := serverThemeArchive(t, manifest, nil)

	imported := importThemeRequest(t, app, archive, false)
	if imported.Code != http.StatusCreated {
		t.Fatalf("theme import returned %d: %s", imported.Code, imported.Body.String())
	}
	var mutation themeMutationResponse
	if err := json.NewDecoder(imported.Body).Decode(&mutation); err != nil {
		t.Fatal(err)
	}
	if mutation.Theme.ID != manifest.ID || mutation.Theme.Source != themes.SourceLocal || !mutation.Theme.Deletable {
		t.Fatalf("unexpected imported theme metadata: %+v", mutation.Theme)
	}
	if info, err := os.Stat(filepath.Join(home, "themes", manifest.ID)); err != nil || !info.IsDir() {
		t.Fatalf("theme was not installed below the configured home directory: info=%v err=%v", info, err)
	}

	duplicate := importThemeRequest(t, app, archive, false)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate theme import returned %d: %s", duplicate.Code, duplicate.Body.String())
	}
	replaced := importThemeRequest(t, app, archive, true)
	if replaced.Code != http.StatusCreated {
		t.Fatalf("replacement theme import returned %d: %s", replaced.Code, replaced.Body.String())
	}

	injectionArchive := serverThemeArchive(t, serverThemeManifest("unsafe-extra-file"), map[string][]byte{
		"theme.css": []byte(`body { background: url("https://example.test/tracker") }`),
	})
	rejected := importThemeRequest(t, app, injectionArchive, false)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("archive carrying arbitrary CSS returned %d: %s", rejected.Code, rejected.Body.String())
	}

	deleteBundled := newTestRequest(http.MethodDelete, "/api/themes/argentina-spain-final", nil)
	deleteBundled.Header.Set(localTokenHeader, app.localToken)
	bundledResult := httptest.NewRecorder()
	app.Routes().ServeHTTP(bundledResult, deleteBundled)
	if bundledResult.Code != http.StatusConflict {
		t.Fatalf("bundled theme deletion returned %d: %s", bundledResult.Code, bundledResult.Body.String())
	}

	deleteLocal := newTestRequest(http.MethodDelete, "/api/themes/"+manifest.ID, nil)
	deleteLocal.Header.Set(localTokenHeader, app.localToken)
	deleted := httptest.NewRecorder()
	app.Routes().ServeHTTP(deleted, deleteLocal)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("local theme deletion returned %d: %s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(filepath.Join(home, "themes", manifest.ID)); !os.IsNotExist(err) {
		t.Fatalf("deleted theme directory still exists: %v", err)
	}
}

func TestThemeRoutesImportReportsUpdateSemanticsAndWarnings(t *testing.T) {
	home := t.TempDir()
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}}, nil, nil, nil)
	manifest := serverThemeManifest("update-flow")
	// Muted text nearly identical to the canvas: readable nowhere.
	manifest.Tokens.Muted = "#0A1524"
	first := importThemeRequest(t, app, serverThemeArchive(t, manifest, nil), false)
	if first.Code != http.StatusCreated {
		t.Fatalf("first import returned %d: %s", first.Code, first.Body.String())
	}
	var firstResponse themeMutationResponse
	if err := json.NewDecoder(first.Body).Decode(&firstResponse); err != nil {
		t.Fatal(err)
	}
	if firstResponse.Replaced || firstResponse.PreviousVersion != "" {
		t.Fatalf("fresh import reported an update: %+v", firstResponse)
	}
	if len(firstResponse.Warnings) == 0 {
		t.Fatalf("unreadable palette produced no contrast warnings: %+v", firstResponse)
	}

	duplicate := importThemeRequest(t, app, serverThemeArchive(t, manifest, nil), false)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), "installed version 1.0.0") {
		t.Fatalf("duplicate import returned %d: %s", duplicate.Code, duplicate.Body.String())
	}

	updated := manifest
	updated.Version = "2.0.0"
	replaced := importThemeRequest(t, app, serverThemeArchive(t, updated, nil), true)
	if replaced.Code != http.StatusCreated {
		t.Fatalf("update import returned %d: %s", replaced.Code, replaced.Body.String())
	}
	var replacedResponse themeMutationResponse
	if err := json.NewDecoder(replaced.Body).Decode(&replacedResponse); err != nil {
		t.Fatal(err)
	}
	if !replacedResponse.Replaced || replacedResponse.PreviousVersion != "1.0.0" || replacedResponse.Theme.Version != "2.0.0" {
		t.Fatalf("update semantics missing from response: %+v", replacedResponse)
	}
}

func TestThemeRoutesCreateManifestAndExport(t *testing.T) {
	home := t.TempDir()
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: home}}, nil, nil, nil)
	manifest := serverThemeManifest("editor-draft")
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]json.RawMessage{"manifest": manifestJSON})
	if err != nil {
		t.Fatal(err)
	}

	// The create endpoint mutates the store, so it must sit behind the same
	// local-token guard as import and delete.
	unguarded := httptest.NewRecorder()
	app.Routes().ServeHTTP(unguarded, newTestRequest(http.MethodPost, "/api/themes", bytes.NewReader(payload)))
	if unguarded.Code != http.StatusUnauthorized && unguarded.Code != http.StatusForbidden {
		t.Fatalf("unguarded theme create returned %d: %s", unguarded.Code, unguarded.Body.String())
	}

	createRequest := newTestRequest(http.MethodPost, "/api/themes", bytes.NewReader(payload))
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set(localTokenHeader, app.localToken)
	created := httptest.NewRecorder()
	app.Routes().ServeHTTP(created, createRequest)
	if created.Code != http.StatusCreated {
		t.Fatalf("theme create returned %d: %s", created.Code, created.Body.String())
	}
	var mutation themeMutationResponse
	if err := json.NewDecoder(created.Body).Decode(&mutation); err != nil {
		t.Fatal(err)
	}
	if mutation.Theme.ID != manifest.ID || mutation.Theme.Source != themes.SourceLocal {
		t.Fatalf("created theme metadata: %+v", mutation.Theme)
	}

	manifestRequest := newTestRequest(http.MethodGet, "/api/themes/"+manifest.ID+"/manifest", nil)
	fetched := httptest.NewRecorder()
	app.Routes().ServeHTTP(fetched, manifestRequest)
	if fetched.Code != http.StatusOK {
		t.Fatalf("theme manifest returned %d: %s", fetched.Code, fetched.Body.String())
	}
	var manifestResponse themeManifestResponse
	if err := json.NewDecoder(fetched.Body).Decode(&manifestResponse); err != nil {
		t.Fatal(err)
	}
	if manifestResponse.Manifest.ID != manifest.ID || manifestResponse.Manifest.Tokens.Canvas != manifest.Tokens.Canvas {
		t.Fatalf("manifest response mismatch: %+v", manifestResponse.Manifest)
	}

	exportRequest := newTestRequest(http.MethodGet, "/api/themes/"+manifest.ID+"/export", nil)
	exported := httptest.NewRecorder()
	app.Routes().ServeHTTP(exported, exportRequest)
	if exported.Code != http.StatusOK {
		t.Fatalf("theme export returned %d: %s", exported.Code, exported.Body.String())
	}
	if got := exported.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("export content type = %q", got)
	}
	if got := exported.Header().Get("Content-Disposition"); !strings.Contains(got, manifest.ID+"-1.0.0.autoto-theme") {
		t.Fatalf("export disposition = %q", got)
	}
	archive := exported.Body.Bytes()
	if _, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive))); err != nil {
		t.Fatalf("export is not a readable ZIP: %v", err)
	}

	// The exported package must survive a round trip through import.
	deleteRequest := newTestRequest(http.MethodDelete, "/api/themes/"+manifest.ID, nil)
	deleteRequest.Header.Set(localTokenHeader, app.localToken)
	deleted := httptest.NewRecorder()
	app.Routes().ServeHTTP(deleted, deleteRequest)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("theme delete returned %d: %s", deleted.Code, deleted.Body.String())
	}
	reimported := importThemeRequest(t, app, archive, false)
	if reimported.Code != http.StatusCreated {
		t.Fatalf("re-import of export returned %d: %s", reimported.Code, reimported.Body.String())
	}

	invalidPayload := []byte(`{"manifest":{"schemaVersion":9}}`)
	invalidRequest := newTestRequest(http.MethodPost, "/api/themes", bytes.NewReader(invalidPayload))
	invalidRequest.Header.Set("Content-Type", "application/json")
	invalidRequest.Header.Set(localTokenHeader, app.localToken)
	invalid := httptest.NewRecorder()
	app.Routes().ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid manifest create returned %d: %s", invalid.Code, invalid.Body.String())
	}

	missingExport := httptest.NewRecorder()
	app.Routes().ServeHTTP(missingExport, newTestRequest(http.MethodGet, "/api/themes/never-installed/export", nil))
	if missingExport.Code != http.StatusNotFound {
		t.Fatalf("missing theme export returned %d: %s", missingExport.Code, missingExport.Body.String())
	}
}

func TestRestrictedRemoteSessionCanUseButCannotManageThemes(t *testing.T) {
	app := remoteAccessTestServer(t)
	store, err := themes.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app.SetThemeStore(store)
	cookies := loginRemoteAccess(t, app, remoteAccessModeRestricted)

	listRequest := newTestRequest(http.MethodGet, "/api/themes", nil)
	listRequest.Host = "remote.example.test"
	markRemoteHTTPS(listRequest)
	for _, cookie := range cookies {
		listRequest.AddCookie(cookie)
	}
	listed := httptest.NewRecorder()
	app.Routes().ServeHTTP(listed, listRequest)
	if listed.Code != http.StatusOK {
		t.Fatalf("restricted remote theme list returned %d: %s", listed.Code, listed.Body.String())
	}

	bundled, err := store.Get("argentina-spain-final")
	if err != nil {
		t.Fatal(err)
	}
	styleRequest := newTestRequest(http.MethodGet, bundled.StylesheetURL, nil)
	styleRequest.Host = "remote.example.test"
	markRemoteHTTPS(styleRequest)
	for _, cookie := range cookies {
		styleRequest.AddCookie(cookie)
	}
	stylesheet := httptest.NewRecorder()
	app.Routes().ServeHTTP(stylesheet, styleRequest)
	if stylesheet.Code != http.StatusOK {
		t.Fatalf("restricted remote stylesheet returned %d: %s", stylesheet.Code, stylesheet.Body.String())
	}

	body, contentType := serverThemeMultipart(t, serverThemeArchive(t, serverThemeManifest("remote-denied"), nil), false)
	importRequest := newTestRequest(http.MethodPost, "/api/themes/import", body)
	importRequest.Host = "remote.example.test"
	markRemoteHTTPS(importRequest)
	importRequest.Header.Set("Content-Type", contentType)
	importRequest.Header.Set(localTokenHeader, app.localToken)
	for _, cookie := range cookies {
		importRequest.AddCookie(cookie)
	}
	denied := httptest.NewRecorder()
	app.Routes().ServeHTTP(denied, importRequest)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("restricted remote theme import returned %d: %s", denied.Code, denied.Body.String())
	}
}

func importThemeRequest(t *testing.T, app *Server, archive []byte, replace bool) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := serverThemeMultipart(t, archive, replace)
	request := newTestRequest(http.MethodPost, "/api/themes/import", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set(localTokenHeader, app.localToken)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	return recorder
}

func serverThemeMultipart(t *testing.T, archive []byte, replace bool) (*bytes.Reader, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "fixture.autoto-theme")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatal(err)
	}
	if replace {
		if err := writer.WriteField("replace", "true"); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(body.Bytes()), writer.FormDataContentType()
}

func serverThemeArchive(t *testing.T, manifest themes.Manifest, extras map[string][]byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	manifestFile, err := writer.Create(themes.ManifestFilename)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manifestFile.Write(manifestBytes); err != nil {
		t.Fatal(err)
	}
	for name, content := range extras {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func serverThemeManifest(id string) themes.Manifest {
	material := themes.Material{Kind: themes.MaterialTranslucent, Opacity: 0.94, Blur: 10, Radius: 16, Shadow: themes.ShadowMedium}
	return themes.Manifest{
		SchemaVersion: themes.SchemaVersionV1,
		ID:            id,
		Name:          "Fixture Match Night",
		Version:       "1.0.0",
		Description:   "A controlled local theme fixture.",
		Author:        "Autoto Tests",
		ColorScheme:   themes.ColorSchemeDark,
		Tokens: themes.Tokens{
			Canvas: "#07111F", Sidebar: "#0A1C30", Card: "#10253C", Input: "#173451",
			Text: "#F7FBFF", Muted: "#9DB1C8", Border: "#75AADB", Primary: "#75AADB",
			Secondary: "#F1BF00", Danger: "#AA151B", Terminal: "#090A0C", Message: "#132B47",
		},
		Materials: themes.Materials{
			Canvas: material, Sidebar: material, Card: material,
			Input: material, Terminal: material, Message: material,
		},
	}
}
